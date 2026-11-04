package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agentmode"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetryread"
)

const (
	defaultTail        = 100
	defaultWaitTimeout = 30 * time.Second
	maxWaitTimeout     = 10 * time.Minute
	// maxLookbackMinutes is the longest look-back the graph accepts (30 days).
	// The server holds it to the plan's retention.
	maxLookbackMinutes = 43200
)

// readFlags are the flags logs, errors and events share.
type readFlags struct {
	scope         scopeFlags
	since, until  string
	tail          int
	before        string
	levels        []string
	grep          string
	ignoreCase    bool
	correlationID string
	fields        []string
	name          string
	untilMatch    string
	minEvents     int
	timeout       time.Duration
	follow        bool
	output        string
	maxPayload    int
}

// readOpts are validated read flags.
type readOpts struct {
	kind       telemetryread.Kind
	filter     telemetryread.Filter // scope fields filled in once resolved
	tail       int
	before     string
	untilMatch *regexp.Regexp
	minEvents  int
	timeout    time.Duration
	follow     bool
	format     string
	maxPayload int
	window     int // requested look-back, minutes
	warnings   []warning
}

func (o readOpts) waiting() bool { return o.untilMatch != nil || o.minEvents > 0 }

func newLogsCmd() *cobra.Command {
	return newReadCmd(telemetryread.KindLog, "logs", "Query or stream the driver's log entries")
}

func newErrorsCmd() *cobra.Command {
	return newReadCmd(telemetryread.KindError, "errors", "Query or stream the driver's error occurrences")
}

func newEventsCmd() *cobra.Command {
	return newReadCmd(telemetryread.KindEvent, "events", "Query or stream the driver's events")
}

// newReadCmd builds one of the three telemetry readers. They share a flag set,
// a query path and a renderer, and differ only in entry type and a few
// type-specific filters.
func newReadCmd(kind telemetryread.Kind, use, short string) *cobra.Command {
	var f readFlags
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long: short + ` from Driverforge, scoped to one device and, with --version, one deploy.

With no flags inside a driver repo it reads the project in .driverforge/config.json
and the device chosen with ` + "`driverforge device select`" + `. --org, --project,
--device and --version are enough on their own, for CI. Entries print oldest first.

Output is text on a terminal and JSON otherwise (and under a coding agent); stdout
carries data only. Exit status: 0 ok, 1 failure, 2 usage, 3 wait timed out,
4 not signed in, 5 scope unresolved or no deploy recorded, 6 rate limited, 130 interrupted.`,
		Example: fmt.Sprintf(`  driverforge %[1]s --version current
  driverforge %[1]s --org acme --project smart-tv --device bench-a --version 12 --tail 50
  driverforge %[1]s --version current --until-match '"data"' --timeout 30s
  driverforge %[1]s --device bench-a --since 10m`, use),
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRead(cmd, kind, f)
		},
	}
	fl := cmd.Flags()
	addScopeFlags(cmd, &f.scope)
	fl.StringVar(&f.scope.device, "device", "", "device: name, hostname, hardware id or id (default: the selected device)")
	fl.StringVar(&f.scope.version, "version", "", "driver version: a number, `current` (this machine's last deploy) or `latest`")
	fl.StringVar(&f.since, "since", "", "start of the window: a duration back from now (30s, 10m, 2h) or an RFC 3339 time")
	fl.StringVar(&f.until, "until", "", "end of the window: a duration back from now or an RFC 3339 time")
	fl.IntVar(&f.tail, "tail", defaultTail, "newest N entries, returned oldest first (max 1000)")
	fl.StringVar(&f.before, "before", "", "page backwards from next_cursor in a previous response")
	fl.StringVar(&f.grep, "grep", "", "regular expression matched against the message and the serialised payload")
	fl.BoolVarP(&f.ignoreCase, "ignore-case", "i", false, "make --grep case-insensitive")
	fl.StringVar(&f.correlationID, "correlation-id", "", "only entries from one event (its trace id)")
	fl.StringArrayVar(&f.fields, "field", nil, "facet filter key=value, repeatable: "+strings.Join(telemetryread.FieldKeys[kind], ", "))
	if kind != telemetryread.KindEvent {
		fl.StringSliceVar(&f.levels, "level", nil, "comma-separated levels, e.g. warn,error")
	} else {
		fl.StringVar(&f.name, "name", "", "event name")
	}
	fl.StringVar(&f.untilMatch, "until-match", "", "wait until an entry matching this regular expression arrives")
	fl.IntVar(&f.minEvents, "min-events", 0, "wait until this many entries pass the filters")
	fl.DurationVar(&f.timeout, "timeout", 0, "longest wait (default 30s with a stop condition, max 10m)")
	fl.BoolVarP(&f.follow, "follow", "f", false, "stream entries as they arrive, until interrupted or --timeout")
	fl.StringVarP(&f.output, "output", "o", "", "output format: text, json or jsonl")
	fl.IntVar(&f.maxPayload, "max-payload-bytes", telemetryread.DefaultMaxPayloadBytes, "cap each entry's message or payload; 0 for no cap")
	return cmd
}

func addScopeFlags(cmd *cobra.Command, s *scopeFlags) {
	cmd.Flags().StringVar(&s.org, "org", "", "organization slug, name or id (default: the repo's, then the selected org)")
	cmd.Flags().StringVar(&s.project, "project", "", "project slug, name or id (default: the repo's)")
}

// runRead is logs, errors and events.
func runRead(cmd *cobra.Command, kind telemetryread.Kind, f readFlags) error {
	stdout, stderr := cmd.OutOrStdout(), cmd.ErrOrStderr()
	diag := diagnostics{w: stderr, json: agentmode.Enabled()}
	o, err := f.validate(kind, stdout, time.Now())
	if err != nil {
		return reportFailure(err, o.format, stdout, diag)
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()
	api := driverforgeapi.New()
	scope, err := resolveScope(ctx, api, f.scope, true)
	if err != nil {
		return reportFailure(err, o.format, stdout, diag)
	}
	o.filter.Controller = scope.device.mac
	o.filter.DriverVersion = scope.version
	src := telemetryread.GraphSource{
		API: api, Filter: o.filter, ProjectIDs: []string{scope.project.ID},
		TimeRangeMinutes: o.window, Names: scope.names,
	}
	if o.follow || o.waiting() {
		return runWait(ctx, api, scope, src, o, stdout, diag)
	}

	res, err := telemetryread.Fetch(ctx, src, telemetryread.Query{Filter: o.filter, Tail: o.tail, Before: o.before})
	if err != nil {
		return reportFailure(err, o.format, stdout, diag)
	}
	writeResult(stdout, diag, o.format, result{
		scope:    scope.out(o.filter.Since, o.filter.Until),
		entries:  res.Entries,
		warnings: append(o.warnings, readWarnings(o, scope, res.Entries, res.WindowMinutes)...),
		hasMore:  res.HasMore,
		cursor:   res.NextCursor,
	})
	return nil
}

// readWarnings truncates entries and works out the warnings a finished read
// carries: payload_truncated, retention_clamped and no_entries.
func readWarnings(o readOpts, scope *telemetryScope, entries []telemetryread.Entry, windowMinutes int) []warning {
	var out []warning
	if w := truncateEntries(entries, o.maxPayload, scope); w != nil {
		out = append(out, *w)
	}
	if !o.filter.Since.IsZero() && windowMinutes > 0 && windowMinutes < o.window {
		out = append(out, warning{Code: warnRetentionClamped,
			Message: fmt.Sprintf("--since reaches past your plan's retention; only the last %s were searched.", time.Duration(windowMinutes)*time.Minute),
			Details: map[string]any{"window_minutes": windowMinutes}})
	}
	if len(entries) == 0 {
		w := warning{Code: warnNoEntries, Message: "No entries matched."}
		if scope.version != "" {
			w.Message = "No entries for version " + scope.version + ". If you just deployed, the driver may not have started or telemetry may not have arrived yet. Re-run with --until-match and --timeout 30s."
		}
		out = append(out, w)
	}
	return out
}

// reportFailure writes a failure in the contract's shape and returns the exit.
// Under an agent, stderr gets one JSON line; in the json formats stdout gets
// the error object, so it always parses. A person at a terminal gets the
// usual card, and an unexpected failure the usual crash report.
func reportFailure(err error, format string, stdout io.Writer, diag diagnostics) error {
	e := classify(err)
	if format == formatJSON || format == formatJSONL {
		writeJSON(stdout, map[string]any{"error": e})
	}
	if diag.json {
		diag.err(e)
		return silentExit(e.exit())
	}
	if e.Code == codeInternal && e.cause != nil {
		return e.cause // a bug: the crash card and the offer to report it
	}
	return e.asUserError()
}
