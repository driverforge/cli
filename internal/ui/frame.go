package ui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The run frame is the single in-place region a build/deploy/sync occupies for
// its whole run: a left-bordered block whose first line is the current step
// (spinner → ✓/⚠/✗), followed by summary rows that accumulate as facts become
// known. The border colour tracks the outcome: grey while running, green on
// success, amber on a degraded success, red on failure. Every step also lands
// in a timestamped log, hidden behind a tab toggle (auto-revealed on failure),
// so the frame stays calm without losing the chronology.

// frameLogTail is how many log lines stay visible while the run is live: an
// inline Bubble Tea region taller than the terminal repaints badly, so the
// live view shows a tail and the full log only lands in scrollback with the
// final render (after which nothing repaints).
const frameLogTail = 12

// frameStepMsg replaces the frame's current-step line.
type frameStepMsg struct{ label string }

// frameLogMsg appends a timestamped entry to the details log. warn entries
// render amber and degrade a successful outcome to ⚠.
type frameLogMsg struct {
	t    time.Time
	text string
	warn bool
}

// frameRowMsg upserts a summary row (ordered by first appearance of the key).
type frameRowMsg struct{ key, val string }

// frameDoneMsg signals the work finished (err non-nil on failure).
type frameDoneMsg struct{ err error }

// frameOutcome is the frame's terminal state, driving the accent colour.
type frameOutcome int

const (
	frameRunning frameOutcome = iota
	frameOK
	frameWarn
	frameFail
)

// frameRunningAccent is the border colour while the run is still in flight —
// the muted grey the rest of the theme uses for subdued content.
var frameRunningAccent = lipgloss.Color("241")

type frameLogEntry struct {
	t    time.Time
	text string
	warn bool
}

// FrameModel is the Bubble Tea model behind the run frame. It is fed
// frame*Msg values by a Frame handle from the work goroutine.
type FrameModel struct {
	step    string
	rows    [][2]string
	log     []frameLogEntry
	details bool
	warned  bool
	outcome frameOutcome
	done    bool
	settle  probeSettle // defers the quit until the terminal's probe replies land
	err     error
	spinner spinner.Model
}

// NewFrameModel creates a run-frame model with the given initial step label.
// detailsOpen starts with the log expanded (--verbose).
func NewFrameModel(step string, detailsOpen bool) *FrameModel {
	return &FrameModel{step: step, details: detailsOpen, spinner: newSpinner()}
}

func (m *FrameModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *FrameModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case frameStepMsg:
		if !m.done {
			m.step = msg.label
		}
		return m, nil
	case frameLogMsg:
		m.log = append(m.log, frameLogEntry(msg))
		if msg.warn {
			m.warned = true
		}
		return m, nil
	case frameRowMsg:
		m.upsertRow(msg.key, msg.val)
		return m, nil
	case frameDoneMsg:
		if m.done { // a ctrl+c already resolved the frame; keep its outcome
			return m, nil
		}
		m.done = true
		m.err = msg.err
		switch {
		case msg.err != nil:
			m.outcome = frameFail
			// Reveal the chronology so the failing run's context is right there
			// in scrollback, above the red frame and the error card.
			m.details = true
		case m.warned:
			m.outcome = frameWarn
		default:
			m.outcome = frameOK
		}
		return m, m.settle.Finish()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		switch msg.String() {
		case "ctrl+c":
			// The sentinel makes ExpectedIfAborted map this to a friendly
			// "Cancelled" card instead of a reportable crash. No log auto-expand:
			// "No changes were made" shouldn't dump a build log.
			m.err = tea.ErrInterrupted
			m.outcome = frameFail
			m.done = true
			return m, tea.Quit
		case "tab":
			if !m.done {
				m.details = !m.details
			}
		}
	}
	return m, nil
}

func (m *FrameModel) upsertRow(key, val string) {
	for i, kv := range m.rows {
		if kv[0] == key {
			m.rows[i][1] = val
			return
		}
	}
	m.rows = append(m.rows, [2]string{key, val})
}

func (m *FrameModel) View() tea.View {
	var b strings.Builder
	if m.details && len(m.log) > 0 {
		b.WriteString(m.renderLog() + "\n\n")
	}
	b.WriteString(calloutBlock(m.accent(), m.frameRows()) + "\n")
	if !m.done {
		b.WriteString("\n" + valStyle.Render("tab") + mutedStyle.Render(" "+m.detailsLabel()) + "\n")
	}
	return tea.NewView(b.String())
}

func (m *FrameModel) detailsLabel() string {
	if m.details {
		return "Hide Details"
	}
	return "View Details"
}

// renderLog renders the details log: the full chronology once done (safe —
// nothing repaints after quit), a tail of frameLogTail lines while live.
func (m *FrameModel) renderLog() string {
	entries := m.log
	var lines []string
	if !m.done && len(entries) > frameLogTail {
		lines = append(lines, mutedStyle.Render(fmt.Sprintf("… %d earlier lines", len(entries)-frameLogTail)))
		entries = entries[len(entries)-frameLogTail:]
	}
	width := frameWidth()
	for _, e := range entries {
		text := e.text
		if e.warn {
			text = warnStyle.Render(text)
		}
		line := mutedStyle.Render(e.t.Format("15:04:05")) + "  " + text
		lines = append(lines, lipgloss.NewStyle().Width(width).Render(line))
	}
	return strings.Join(lines, "\n")
}

// frameRows assembles the callout body: the current-step line, then (once any
// summary rows exist) a spacer and the aligned key/value rows — the same shape
// SuccessBlock renders.
func (m *FrameModel) frameRows() []string {
	width := frameWidth()
	rows := []string{lipgloss.NewStyle().Width(width).Render(m.stepIcon() + " " + m.step)}
	if len(m.rows) == 0 {
		return rows
	}
	keyWidth := 0
	for _, kv := range m.rows {
		if len(kv[0]) > keyWidth {
			keyWidth = len(kv[0])
		}
	}
	rows = append(rows, "")
	for _, kv := range m.rows {
		row := keyStyle.Render(fmt.Sprintf("%-*s ", keyWidth, kv[0])) + valStyle.Render(kv[1])
		rows = append(rows, lipgloss.NewStyle().Width(width).Render(row))
	}
	return rows
}

func (m *FrameModel) stepIcon() string {
	switch m.outcome {
	case frameOK:
		return okStyle.Render("✓")
	case frameWarn:
		return warnStyle.Render("⚠")
	case frameFail:
		return errStyle.Render("✗")
	default:
		return m.spinner.View()
	}
}

func (m *FrameModel) accent() color.Color {
	switch m.outcome {
	case frameOK:
		return colOk
	case frameWarn:
		return colAmber
	case frameFail:
		return colRed
	default:
		return frameRunningAccent
	}
}

// Err returns the run error after the program has exited.
func (m *FrameModel) Err() error { return m.err }

// FrameOptions configures a run frame.
type FrameOptions struct {
	Step        string // initial current-step label, e.g. "Building test…"
	DetailsOpen bool   // start with the log expanded (--verbose)
}

// Frame is the handle the work goroutine drives the frame through. All methods
// are goroutine-safe (they post messages into the program).
type Frame struct{ p *tea.Program }

// Step replaces the frame's current-step line.
func (f *Frame) Step(label string) { f.p.Send(frameStepMsg{label: label}) }

// Logf appends a timestamped entry to the details log.
func (f *Frame) Logf(format string, a ...any) {
	f.p.Send(frameLogMsg{t: time.Now(), text: fmt.Sprintf(format, a...)})
}

// Warnf appends an amber log entry and degrades a successful outcome to ⚠.
func (f *Frame) Warnf(format string, a ...any) {
	f.p.Send(frameLogMsg{t: time.Now(), text: fmt.Sprintf(format, a...), warn: true})
}

// Row upserts a summary row in the frame body.
func (f *Frame) Row(key, val string) { f.p.Send(frameRowMsg{key: key, val: val}) }

// RunFrame runs work under a live run frame, returning its error. The work
// gets a child context cancelled on abort (so a Ctrl-C also stops the work)
// and a Frame handle to report progress through. An interrupt maps to the
// friendly Cancelled via ExpectedIfAborted; everything else returns unchanged,
// so the top-level handler renders the usual card below the frame's final
// render.
func RunFrame(ctx context.Context, opts FrameOptions, work func(ctx context.Context, f *Frame) error) error {
	m := NewFrameModel(opts.Step, opts.DetailsOpen)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	var workErr error
	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		workErr = work(ctx, &Frame{p: p})
		p.Send(frameDoneMsg{err: workErr})
	}()
	_, runErr := p.Run()
	// Stop the work and wait for its goroutine before reading workErr.
	cancel()
	<-workDone
	if runErr != nil {
		return ExpectedIfAborted(runErr)
	}
	if errors.Is(m.err, tea.ErrInterrupted) {
		return ExpectedIfAborted(m.err)
	}
	return workErr
}
