package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agentmode"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/ui"
)

// Structured output.
//
// Under a coding agent, or with DRIVERFORGE_OUTPUT=json, every command writes
// for a program rather than a person:
//
//   - stdout carries one JSON document: the command's result, or {"error": …}
//     when it fails. Commands that stream (logs --follow, debug) write one JSON
//     object per line instead.
//   - stderr carries JSON lines: {"progress": …} as work happens, then
//     {"error": …} if it fails.
//   - nothing prompts: a question a person would have answered fails with
//     input_required, naming the question.
//
// A command writes its human-readable lines to textOut, which under structured
// output becomes a progressWriter turning each line into a progress line, so
// stdout stays one document; and it reports its outcome through emit. Work that
// runs under a run frame goes through runFrame, which hands it a ui.Progress
// writing progress lines when there's no frame to draw. The telemetry readers
// predate this and keep their own documented shapes.

// structured reports whether this run writes structured output.
func structured() bool { return agentmode.Structured() }

// textOut is where a command writes its human-readable lines: stdout, or
// progress lines on stderr under structured output.
func textOut(cmd *cobra.Command) io.Writer {
	if structured() {
		return newProgressWriter(cmd.ErrOrStderr())
	}
	return cmd.OutOrStdout()
}

// emit reports a command's outcome: doc as the JSON document on stdout under
// structured output, otherwise whatever text writes for a person.
func emit(cmd *cobra.Command, doc any, text func(w io.Writer) error) error {
	if structured() {
		writeJSON(cmd.OutOrStdout(), doc)
		return nil
	}
	return text(cmd.OutOrStdout())
}

// noText is emit's text for a command whose text output has already been
// written as it went.
func noText(io.Writer) error { return nil }

// Progress statuses.
const (
	statusRunning = "running" // a step began
	statusOK      = "ok"      // a step finished
	statusSkipped = "skipped" // a step had nothing to do
	statusFailed  = "failed"  // a step failed; the error follows
	statusWarning = "warning" // worth knowing, but the command carries on
	statusInfo    = "info"    // anything else
)

type progressLine struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// progressWriter turns text into progress lines: each line written to it
// becomes {"progress": {"status", "message"}} on w, with colour and layout
// stripped and a leading status glyph (✓ ✗ ⚠ – →) read as the status. Safe for
// concurrent writers.
type progressWriter struct {
	mu       sync.Mutex
	w        io.Writer
	buf      []byte
	warnings []warning
}

func newProgressWriter(w io.Writer) *progressWriter { return &progressWriter{w: w} }

func (p *progressWriter) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.buf = append(p.buf, b...)
	for {
		i := bytes.IndexByte(p.buf, '\n')
		if i < 0 {
			return len(b), nil
		}
		line := string(p.buf[:i])
		p.buf = p.buf[i+1:]
		if status, msg, ok := parseLine(line); ok {
			p.writeLocked(status, msg)
		}
	}
}

// emit writes one progress line.
func (p *progressWriter) emit(status, msg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writeLocked(status, msg)
}

func (p *progressWriter) writeLocked(status, msg string) {
	if status == statusWarning {
		p.warnings = append(p.warnings, warning{Code: codeWarning, Message: msg})
	}
	writeJSON(p.w, map[string]any{"progress": progressLine{Status: status, Message: msg}})
}

// reported returns the warnings written so far, for the result document.
func (p *progressWriter) reported() []warning {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]warning(nil), p.warnings...)
}

// codeWarning is the code of a warning a command reported as text.
const codeWarning = "warning"

// lineStatus maps a leading glyph to its status.
var lineStatus = []struct{ glyph, status string }{
	{"✓", statusOK}, {"✗", statusFailed}, {"⚠", statusWarning},
	{"–", statusSkipped}, {"⏺", statusSkipped},
	{"==>", statusRunning}, {"→", statusInfo}, {"←", statusInfo}, {"💡", statusInfo},
}

// parseLine reads one line of text as a progress line. ok is false for a line
// with nothing to say: blank, or only rules and box drawing.
func parseLine(line string) (status, msg string, ok bool) {
	s := strings.TrimSpace(ansi.Strip(line))
	s = strings.TrimSpace(strings.TrimLeft(s, "┃│"))
	if !strings.ContainsFunc(s, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		return "", "", false
	}
	for _, g := range lineStatus {
		if rest, found := strings.CutPrefix(s, g.glyph); found {
			return g.status, strings.TrimSpace(rest), true
		}
	}
	// Work under way is labelled with a trailing ellipsis ("Waiting for
	// authorization…"), as a frame's steps are.
	if running, found := strings.CutSuffix(s, "…"); found {
		return statusRunning, running, true
	}
	return statusInfo, s, true
}

// progressFrame is the ui.Progress a run frame's work reports through under
// structured output: steps, log lines and warnings become progress lines.
// Summary rows aren't written — the command's result document carries them.
type progressFrame struct{ p *progressWriter }

// Step reports the frame's current step. A step under way is labelled with a
// trailing ellipsis ("Connecting to bench-a…"); one without is where the work
// ended ("Build complete").
func (f progressFrame) Step(label string) {
	if running, found := strings.CutSuffix(label, "…"); found {
		f.p.emit(statusRunning, running)
		return
	}
	f.p.emit(statusOK, label)
}

func (f progressFrame) Logf(format string, a ...any) {
	if status, msg, ok := parseLine(fmt.Sprintf(format, a...)); ok {
		f.p.emit(status, msg)
	}
}

func (f progressFrame) Warnf(format string, a ...any) {
	_, msg, ok := parseLine(fmt.Sprintf(format, a...))
	if ok {
		f.p.emit(statusWarning, msg)
	}
}

func (f progressFrame) Row(string, string) {}

// runFrame runs work under a live run frame — or, when out is structured
// output's progressWriter, reporting progress lines through it — and returns
// the warnings the work reported (none for a frame, which shows them).
func runFrame(ctx context.Context, out io.Writer, opts ui.FrameOptions, work func(ctx context.Context, p ui.Progress) error) ([]warning, error) {
	if pw, ok := out.(*progressWriter); ok {
		pw.emit(statusRunning, strings.TrimSuffix(opts.Step, "…"))
		err := work(ctx, progressFrame{pw})
		return pw.reported(), err
	}
	return nil, ui.RunFrame(ctx, opts, func(ctx context.Context, f *ui.Frame) error { return work(ctx, f) })
}

// writeFailure reports a failed command under structured output: the error
// document on stdout, so it always parses, and the same error as a line on
// stderr. A silent error has been reported already. It returns the exit status.
func writeFailure(stdout, stderr io.Writer, err error) int {
	if clierr.IsSilent(err) {
		return exitFor(err)
	}
	e := describe(err, commandFailure)
	writeJSON(stdout, map[string]any{"error": e})
	writeJSON(stderr, map[string]any{"error": e})
	return e.exit()
}
