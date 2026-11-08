package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/telemetryread"
)

// kindErrorGroup is get's --type for an error group. It isn't an entry kind:
// a group is every occurrence of one error, not one record.
const kindErrorGroup = "error_group"

// refRE is an error group's reference, <PROJECT-SLUG>-<FINGERPRINT>: the
// fingerprint is eight hex characters and never holds a hyphen, so the split
// is on the last one. Any casing.
var refRE = regexp.MustCompile(`^(.+)-([0-9a-fA-F]{8})$`)

// errorRef is a parsed error group reference.
type errorRef struct {
	ref, project, fingerprint string
}

func parseErrorRef(id string) (errorRef, bool) {
	m := refRE.FindStringSubmatch(strings.TrimSpace(id))
	if m == nil {
		return errorRef{}, false
	}
	return errorRef{ref: strings.ToUpper(m[0]), project: strings.ToLower(m[1]), fingerprint: strings.ToLower(m[2])}, true
}

func validateGroupGet(id string, maxPayload int) error {
	if _, ok := parseErrorRef(id); !ok {
		return newCmdErr(codeUsage, "That isn't an error group reference.",
			"Pass the reference the web app shows, <PROJECT-SLUG>-<FINGERPRINT>, such as POOL-PUMP-4F2A9C1B.").with("id", id)
	}
	if maxPayload > 0 {
		return newCmdErr(codeUsage, "--max-payload-bytes applies to entries, not error groups.", "Drop --max-payload-bytes.")
	}
	return nil
}

// runGetGroup fetches an error group by reference. The reference names its
// project, so it scopes the read; a --project naming another one is an error.
func runGetGroup(ctx context.Context, ref errorRef, sf scopeFlags, format string, stdout io.Writer, diag diagnostics) error {
	explicit := sf.project
	if explicit == "" {
		sf.project = ref.project
	}
	api := driverforgeapi.New()
	scope, err := resolveScope(ctx, api, sf, false)
	if err == nil && explicit != "" && !strings.EqualFold(scope.project.Slug, ref.project) {
		err = newCmdErr(codeUsage, ref.ref+" belongs to project "+ref.project+", not "+scope.project.Slug+".",
			"Drop --project; the reference names its project.").with("id", ref.ref)
	}
	if err != nil {
		return reportFailure(err, format, stdout, diag)
	}
	g, err := api.ErrorGroup(ctx, scope.project.ID, ref.fingerprint)
	if err == nil && g == nil {
		err = newCmdErr(codeEntryNotFound, "No error group "+ref.ref+" in "+scope.project.Slug+".",
			"Check the reference; errors older than your plan's retention are gone.").with("id", ref.ref)
	}
	if err != nil {
		return reportFailure(err, format, stdout, diag)
	}
	out := newErrorGroupOut(*g, ref, scope)
	so := scope.out(time.Time{}, time.Time{})
	if format == formatJSON {
		writeJSON(stdout, map[string]any{"scope": so, "error_group": out, "warnings": []warning{}})
		return nil
	}
	diag.scope(so)
	out.writeText(stdout)
	return nil
}

// errorGroupOut is an error group in get's output.
type errorGroupOut struct {
	Ref              string                 `json:"ref"`
	Fingerprint      string                 `json:"fingerprint"`
	Type             string                 `json:"type"`
	Message          string                 `json:"message"`
	Count            int                    `json:"count"`
	FirstSeen        string                 `json:"first_seen"`
	LastSeen         string                 `json:"last_seen"`
	DriverVersions   []string               `json:"driver_versions"`
	Drivers          []string               `json:"drivers"`
	Devices          []telemetryread.Device `json:"devices"`
	LatestEvent      *string                `json:"latest_event"`
	LatestStacktrace json.RawMessage        `json:"latest_stacktrace"`
	LatestOccurrence *latestOccurrenceOut   `json:"latest_occurrence"`
}

// latestOccurrenceOut is the group's newest occurrence and the command that
// fetches it with its arguments and last log.
type latestOccurrenceOut struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

func newErrorGroupOut(g driverforgeapi.ErrorGroup, ref errorRef, s *telemetryScope) errorGroupOut {
	o := errorGroupOut{
		Ref: g.Ref, Fingerprint: g.Fingerprint, Type: g.Type, Message: g.Value, Count: g.Count,
		FirstSeen: msRFC3339(g.FirstSeen), LastSeen: msRFC3339(g.LastSeen),
		DriverVersions: nonNilStrings(g.AffectedVersions), Drivers: nonNilStrings(g.AffectedDrivers),
		Devices: []telemetryread.Device{}, LatestEvent: optional(g.LatestEventName),
		LatestStacktrace: g.LatestStacktrace,
	}
	if o.Ref == "" {
		o.Ref = ref.ref
	}
	if len(o.LatestStacktrace) == 0 {
		o.LatestStacktrace = json.RawMessage("null")
	}
	for _, c := range g.AffectedControllers {
		d := telemetryread.Device{ID: c}
		if name := s.names(c); name != "" {
			d.Name = &name
		}
		o.Devices = append(o.Devices, d)
	}
	if g.LatestOccurrenceID != "" {
		o.LatestOccurrence = &latestOccurrenceOut{ID: g.LatestOccurrenceID,
			Command: fmt.Sprintf("driverforge get %s --type error --org %s --project %s", g.LatestOccurrenceID, s.org.Slug, s.project.Slug)}
	}
	return o
}

func (o errorGroupOut) writeText(w io.Writer) {
	fmt.Fprintf(w, "%s  %s  %d occurrence%s\n", o.Ref, o.Type, o.Count, plural(o.Count))
	fmt.Fprintln(w, "  "+o.Message)
	fmt.Fprintf(w, "  first seen %s, last seen %s\n", o.FirstSeen, o.LastSeen)
	if len(o.DriverVersions) > 0 {
		fmt.Fprintln(w, "  versions: "+strings.Join(o.DriverVersions, ", "))
	}
	if len(o.Devices) > 0 {
		names := make([]string, len(o.Devices))
		for i, d := range o.Devices {
			names[i] = d.ID
			if d.Name != nil {
				names[i] = *d.Name
			}
		}
		fmt.Fprintln(w, "  devices: "+strings.Join(names, ", "))
	}
	if o.LatestEvent != nil {
		fmt.Fprintln(w, "  latest in: "+*o.LatestEvent)
	}
	if o.LatestOccurrence != nil {
		fmt.Fprintln(w, "  latest occurrence: "+o.LatestOccurrence.Command)
	}
}

func msRFC3339(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
