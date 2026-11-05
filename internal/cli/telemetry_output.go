package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/telemetryread"
)

// Output formats.
const (
	formatText  = "text"
	formatJSON  = "json"
	formatJSONL = "jsonl"
)

// Stable warning codes.
const (
	warnNoEntries        = "no_entries"
	warnRetentionClamped = "retention_clamped"
	warnPayloadTruncated = "payload_truncated"
	warnTailClamped      = "tail_clamped"
)

type warning struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// scopeOut is the scope a response actually used, echoed in every response so
// a wrong context can't be read silently.
type scopeOut struct {
	Org     string          `json:"org"`
	Project string          `json:"project"`
	Device  *scopeDeviceOut `json:"device"`
	Version *string         `json:"version"`
	Since   *string         `json:"since"`
	Until   *string         `json:"until"`
}

// scopeDeviceOut is the device a telemetry read was scoped to; its id is the
// hardware id entries carry.
type scopeDeviceOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type waitOut struct {
	Satisfied bool   `json:"satisfied"`
	Reason    string `json:"reason"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Transport string `json:"transport"`
}

// envelope is the json format's one document per invocation.
type envelope struct {
	Scope      scopeOut              `json:"scope"`
	Entries    []telemetryread.Entry `json:"entries"`
	Count      int                   `json:"count"`
	HasMore    bool                  `json:"has_more"`
	NextCursor *string               `json:"next_cursor"`
	Warnings   []warning             `json:"warnings"`
	Wait       *waitOut              `json:"wait,omitempty"`
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timeOut(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339Nano)
	return &s
}

func (s *telemetryScope) out(since, until time.Time) scopeOut {
	o := scopeOut{
		Org: s.org.Slug, Project: s.project.Slug,
		Version: optional(s.version),
		Since:   timeOut(since), Until: timeOut(until),
	}
	if s.device != nil {
		o.Device = &scopeDeviceOut{ID: s.device.mac, Name: s.device.name}
	}
	return o
}

// writeJSON writes v as one compact line. Payloads are already JSON and are
// written byte for byte; HTML escaping is off so a payload's "<" stays "<".
func writeJSON(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	if f, ok := w.(interface{ Sync() error }); ok {
		_ = f.Sync()
	}
}

// diagnostics writes everything that isn't data to stderr: JSON lines under an
// agent, plain lines for a person.
type diagnostics struct {
	w    io.Writer
	json bool
}

func (d diagnostics) scope(s scopeOut) {
	if d.json {
		writeJSON(d.w, map[string]any{"scope": s})
		return
	}
	parts := []string{"org=" + s.Org, "project=" + s.Project}
	if s.Device != nil {
		parts = append(parts, "device="+s.Device.Name)
	}
	if s.Version != nil {
		parts = append(parts, "version="+*s.Version)
	}
	if s.Since != nil {
		parts = append(parts, "since="+*s.Since)
	}
	if s.Until != nil {
		parts = append(parts, "until="+*s.Until)
	}
	fmt.Fprintln(d.w, "scope: "+strings.Join(parts, " "))
}

func (d diagnostics) warn(w warning) {
	if d.json {
		writeJSON(d.w, map[string]any{"warning": w})
		return
	}
	fmt.Fprintln(d.w, "warning: "+w.Message)
	if cmds, ok := w.Details["entries"].([]truncatedRef); ok {
		for _, c := range cmds {
			fmt.Fprintln(d.w, "  "+c.Command)
		}
	}
}

func (d diagnostics) wait(w waitOut) {
	if d.json {
		writeJSON(d.w, map[string]any{"wait": w})
		return
	}
	if !w.Satisfied {
		fmt.Fprintf(d.w, "timed out after %s without the stop condition being met\n", time.Duration(w.ElapsedMS)*time.Millisecond)
	}
}

func (d diagnostics) err(e *cmdErr) {
	if d.json {
		writeJSON(d.w, map[string]any{"error": e})
	}
}

// truncatedRef names a truncated entry and the command that fetches it whole.
type truncatedRef struct {
	ID      string `json:"id"`
	Command string `json:"command"`
}

// truncate caps every entry's payload, returning the payload_truncated warning
// when any was cut.
func truncateEntries(entries []telemetryread.Entry, limit int, s *telemetryScope) *warning {
	var refs []truncatedRef
	for i := range entries {
		if telemetryread.Truncate(&entries[i], limit) {
			refs = append(refs, truncatedRef{ID: entries[i].ID, Command: getCommand(entries[i], s)})
		}
	}
	if len(refs) == 0 {
		return nil
	}
	w := truncatedWarning(refs, limit)
	return &w
}

func truncatedWarning(refs []truncatedRef, limit int) warning {
	return warning{
		Code:    warnPayloadTruncated,
		Message: fmt.Sprintf("Cut %d payload%s to --max-payload-bytes %d; fetch each in full with the command listed.", len(refs), plural(len(refs)), limit),
		Details: map[string]any{"entries": refs},
	}
}

// getCommand is the ready-to-run command that fetches e untruncated.
func getCommand(e telemetryread.Entry, s *telemetryScope) string {
	return fmt.Sprintf("driverforge get %s --type %s --org %s --project %s", e.ID, e.Type, s.org.Slug, s.project.Slug)
}

// writeTextEntry renders an entry for a person: a header line, then the
// payload pretty-printed beneath it.
func writeTextEntry(w io.Writer, e telemetryread.Entry) {
	label := strings.ToUpper(string(e.Type))
	if e.Level != nil && e.Type == telemetryread.KindLog {
		label = strings.ToUpper(*e.Level)
	}
	head := []string{e.Timestamp, fmt.Sprintf("%-5s", label)}
	if e.DriverVersion != nil {
		head = append(head, "v"+*e.DriverVersion)
	}
	if e.Name != nil {
		head = append(head, *e.Name)
	}
	if e.Status != nil {
		head = append(head, *e.Status)
	}
	if e.Message != nil {
		head = append(head, *e.Message)
	}
	fmt.Fprintln(w, strings.Join(head, "  "))
	if p := bytes.TrimSpace(e.Payload); len(p) > 0 && !bytes.Equal(p, []byte("null")) {
		var pretty bytes.Buffer
		if json.Indent(&pretty, p, "    ", "  ") == nil {
			fmt.Fprintln(w, "    "+pretty.String())
		} else {
			fmt.Fprintln(w, "    "+string(p))
		}
	}
	if e.Truncated != nil {
		fmt.Fprintf(w, "    … truncated: %d of %d bytes (driverforge get %s --type %s)\n",
			e.Truncated.ReturnedBytes, e.Truncated.TotalBytes, e.ID, e.Type)
	}
}

// result is a finished read, ready to write.
type result struct {
	scope    scopeOut
	entries  []telemetryread.Entry
	warnings []warning
	hasMore  bool
	cursor   string
	wait     *waitOut // set when the read waited on a stop condition
}

// writeResult writes a finished read: one envelope in json; otherwise the
// entries on stdout and the scope, warnings and wait outcome on stderr.
func writeResult(stdout io.Writer, diag diagnostics, format string, r result) {
	if format == formatJSON {
		writeJSON(stdout, envelope{
			Scope: r.scope, Entries: nonNil(r.entries), Count: len(r.entries),
			HasMore: r.hasMore, NextCursor: optional(r.cursor),
			Warnings: nonNilWarnings(r.warnings), Wait: r.wait,
		})
		return
	}
	diag.scope(r.scope)
	writeEntries(stdout, format, r.entries)
	for _, w := range r.warnings {
		diag.warn(w)
	}
	if r.wait != nil {
		diag.wait(*r.wait)
	}
	if r.hasMore && !diag.json {
		fmt.Fprintf(diag.w, "older entries: --before %s\n", r.cursor)
	}
}

// writeEntries writes entries as text or one JSON line each.
func writeEntries(w io.Writer, format string, entries []telemetryread.Entry) {
	for _, e := range entries {
		if format == formatText {
			writeTextEntry(w, e)
		} else {
			writeJSON(w, e)
		}
	}
}

func nonNil(es []telemetryread.Entry) []telemetryread.Entry {
	if es == nil {
		return []telemetryread.Entry{}
	}
	return es
}

func nonNilWarnings(ws []warning) []warning {
	if ws == nil {
		return []warning{}
	}
	return ws
}
