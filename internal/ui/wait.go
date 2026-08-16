package ui

import (
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// waitDoneMsg signals that the background work finished: label is the success
// line to show (empty on failure), err is non-nil on failure.
type waitDoneMsg struct {
	label string
	err   error
}

// waitModel shows a rectangular spinner beside a label while background work
// runs, then replaces the spinner line in place with a ✓ success label — or
// clears it on error/cancel, leaving the caller to render the outcome.
type waitModel struct {
	label   string // shown next to the spinner while waiting
	done    string // shown next to the ✓ once finished successfully
	spinner spinner.Model
	settle  probeSettle // defers the quit until the terminal's probe replies land
	quit    bool
	err     error
}

func (m *waitModel) Init() tea.Cmd { return m.spinner.Tick }

func (m *waitModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case waitDoneMsg:
		m.done = msg.label
		m.err = msg.err
		m.quit = true
		return m, m.settle.Finish()
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.err = tea.ErrInterrupted // mapped to a friendly "Cancelled" by callers
			m.quit = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *waitModel) View() tea.View {
	if m.quit {
		if m.err != nil {
			return tea.NewView("") // clear the spinner line; the caller renders the error
		}
		return tea.NewView(Success(m.done) + "\n")
	}
	return tea.NewView(m.spinner.View() + " " + mutedStyle.Render(m.label) + "\n")
}

// WaitFor runs fn in the background while showing a rectangular spinner beside
// label. fn returns the success line to show in fn's place once done. On
// success the spinner line is replaced in place with a ✓ success line; on error
// or cancel it is cleared and fn's error (or a friendly "Cancelled") is
// returned. When interactive is false it prints label plainly, runs fn, then
// prints the success line — so pipes/CI stay clean.
func WaitFor(ctx context.Context, w io.Writer, interactive bool, label string, fn func() (string, error)) error {
	return waitFor(ctx, w, interactive, true, label, fn)
}

// WaitForStep is WaitFor without the leading blank line — for a step that
// immediately follows another (e.g. "Connecting…" right after "Finding…"), so
// the ✓ lines stack tightly instead of being separated by a gap.
func WaitForStep(ctx context.Context, w io.Writer, interactive bool, label string, fn func() (string, error)) error {
	return waitFor(ctx, w, interactive, false, label, fn)
}

func waitFor(ctx context.Context, w io.Writer, interactive, lead bool, label string, fn func() (string, error)) error {
	// A single leading blank line separates this step from the output above it,
	// matching the spacing forms get from their top margin. A chained step
	// (WaitForStep) omits it so consecutive ✓ lines don't get a gap between them.
	if lead {
		fmt.Fprintln(w)
	}

	if !interactive {
		fmt.Fprintln(w, label)
		done, err := fn()
		if err != nil {
			return err
		}
		fmt.Fprintln(w, Success(done))
		return nil
	}

	m := &waitModel{label: label, spinner: newSpinner()}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(w))
	go func() {
		done, err := fn()
		p.Send(waitDoneMsg{label: done, err: err})
	}()
	if _, err := p.Run(); err != nil {
		return ExpectedIfAborted(err)
	}
	return ExpectedIfAborted(m.err)
}

// newSpinner builds the dot spinner used while waiting, in the brand accent.
func newSpinner() spinner.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = spinnerStyle
	return s
}
