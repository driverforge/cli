package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/driverforge/cli/internal/telemetryread"
)

// outputFormat picks the format: the flag, else text for a person at a
// terminal, else JSON (JSON Lines when following).
func outputFormat(flag string, follow bool, stdout io.Writer) (string, error) {
	switch flag {
	case formatText, formatJSONL:
		return flag, nil
	case formatJSON:
		if follow {
			return formatJSONL, nil // a stream is lines, not one document
		}
		return flag, nil
	case "":
	default:
		return "", newCmdErr(codeUsage, "--output must be text, json or jsonl.", "Pass -o text, -o json or -o jsonl.").with("output", flag)
	}
	if !structured() && isTerminal(stdout) {
		return formatText, nil
	}
	if follow {
		return formatJSONL, nil
	}
	return formatJSON, nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// parseWhen reads a --since/--until value: a duration back from now or an
// RFC 3339 time.
func parseWhen(flag, v string, now time.Time) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	return time.Time{}, newCmdErr(codeUsage, "--"+flag+" must be a duration like 10m or an RFC 3339 time.",
		"For example --"+flag+" 30m or --"+flag+" 2026-11-04T09:00:00Z.").with(flag, v)
}

func compile(flag, pattern string, ignoreCase bool) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	if ignoreCase {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, newCmdErr(codeUsage, "--"+flag+" isn't a valid regular expression.", "Check the pattern's syntax (Go RE2).").with(flag, pattern)
	}
	return re, nil
}

// validate checks the flags without touching the network.
func (f readFlags) validate(kind telemetryread.Kind, stdout io.Writer, now time.Time) (readOpts, error) {
	o := readOpts{kind: kind, before: f.before, minEvents: f.minEvents, follow: f.follow, maxPayload: f.maxPayload}
	var err error
	if o.format, err = outputFormat(f.output, f.follow, stdout); err != nil {
		return o, err
	}

	switch {
	case f.tail < 1:
		return o, newCmdErr(codeUsage, "--tail must be at least 1.", "Pass --tail 1 to --tail 1000.")
	case f.tail > telemetryread.MaxPageSize:
		o.tail = telemetryread.MaxPageSize
		o.warnings = append(o.warnings, warning{Code: warnTailClamped,
			Message: fmt.Sprintf("--tail %d is over the maximum; returning the newest %d. Page further with --before.", f.tail, telemetryread.MaxPageSize)})
	default:
		o.tail = f.tail
	}
	if f.minEvents < 0 {
		return o, newCmdErr(codeUsage, "--min-events can't be negative.", "Pass --min-events 1 or more.")
	}
	if f.maxPayload < 0 {
		return o, newCmdErr(codeUsage, "--max-payload-bytes can't be negative.", "Pass a byte count, or 0 for no cap.")
	}

	filter := telemetryread.Filter{Kind: kind, CorrelationID: f.correlationID, Name: f.name}
	if filter.Since, err = parseWhen("since", f.since, now); err != nil {
		return o, err
	}
	if filter.Until, err = parseWhen("until", f.until, now); err != nil {
		return o, err
	}
	if !filter.Since.IsZero() && !filter.Until.IsZero() && filter.Until.Before(filter.Since) {
		return o, newCmdErr(codeUsage, "--until is before --since.", "Swap them, or widen the window.")
	}
	if filter.Grep, err = compile("grep", f.grep, f.ignoreCase); err != nil {
		return o, err
	}
	if o.untilMatch, err = compile("until-match", f.untilMatch, false); err != nil {
		return o, err
	}
	for _, l := range f.levels {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" {
			filter.Levels = append(filter.Levels, l)
		}
	}
	valid := telemetryread.FieldKeys[kind]
	for _, kv := range f.fields {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !slices.Contains(valid, k) {
			return o, newCmdErr(codeUsage, "--field "+strconv.Quote(kv)+" isn't a key=value facet for "+string(kind)+"s.",
				"Valid keys: "+strings.Join(valid, ", ")+".").with("field", kv).with("valid", valid)
		}
		if filter.Fields == nil {
			filter.Fields = map[string]string{}
		}
		if k == "level" {
			v = strings.ToLower(v)
		}
		filter.Fields[k] = v
	}
	o.filter = filter

	switch {
	case f.timeout > maxWaitTimeout:
		return o, newCmdErr(codeUsage, "--timeout can be at most 10m.", "Pass --timeout 10m or less.")
	case f.timeout < 0:
		return o, newCmdErr(codeUsage, "--timeout can't be negative.", "Pass a duration such as 30s.")
	case f.timeout > 0:
		o.timeout = f.timeout
	case o.waiting():
		o.timeout = defaultWaitTimeout
	}

	o.window = maxLookbackMinutes
	if !filter.Since.IsZero() {
		mins := int(math.Ceil(now.Sub(filter.Since).Minutes())) + 1
		o.window = min(max(mins, 1), maxLookbackMinutes)
	}
	return o, nil
}
