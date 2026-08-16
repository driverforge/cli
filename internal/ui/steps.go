package ui

import (
	"context"
	"fmt"
	"io"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// Step is one unit of work in an execution plan. Run does the work (and may
// capture results into variables the caller closes over for later steps).
type Step struct {
	Label string
	Run   func() error
}

type stepState int

const (
	stepPending stepState = iota
	stepRunning
	stepDone
	stepFailed
)

// RunSteps executes steps in order, rendering a live progress list: a spinner on
// the running step, a green ✓ on completed ones, a red ✗ on a failure. It stops
// at the first failure and returns that error. Non-interactive output degrades to
// plain lines so pipes/CI stay clean.
func RunSteps(ctx context.Context, w io.Writer, interactive bool, steps []Step) error {
	fmt.Fprintln(w) // separate the list from the output above it

	if !interactive {
		for _, s := range steps {
			fmt.Fprintln(w, s.Label+"…")
			if err := s.Run(); err != nil {
				return err
			}
			fmt.Fprintln(w, Success(s.Label))
		}
		return nil
	}

	m := &stepsModel{steps: steps, status: make([]stepState, len(steps)), spinner: newSpinner()}
	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(w))
	if _, err := p.Run(); err != nil {
		return ExpectedIfAborted(err)
	}
	// A ctrl+c quits the program cleanly with m.err = tea.ErrInterrupted, so the
	// model's error needs the same friendly-cancel mapping as p.Run's.
	return ExpectedIfAborted(m.err)
}

type stepDoneMsg struct {
	i   int
	err error
}

// stepsModel drives the progress list: it runs each step in a command and ticks
// the line to ✓/✗ as results arrive, advancing to the next on success.
type stepsModel struct {
	steps    []Step
	status   []stepState
	spinner  spinner.Model
	settle   probeSettle // defers the quit until the terminal's probe replies land
	err      error
	quit     bool
	aborting bool // ctrl+c received while a step was in flight
}

func (m *stepsModel) Init() tea.Cmd {
	if len(m.steps) == 0 {
		return tea.Quit
	}
	m.status[0] = stepRunning
	return tea.Batch(m.spinner.Tick, m.run(0))
}

func (m *stepsModel) run(i int) tea.Cmd {
	return func() tea.Msg { return stepDoneMsg{i: i, err: m.steps[i].Run()} }
}

func (m *stepsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case stepDoneMsg:
		if m.aborting {
			// The user cancelled while this step ran; record its outcome so the
			// list is truthful, but keep the interrupt as the returned error.
			if msg.err != nil {
				m.status[msg.i] = stepFailed
			} else {
				m.status[msg.i] = stepDone
			}
			return m, tea.Quit
		}
		if msg.err != nil {
			m.status[msg.i] = stepFailed
			m.err = msg.err
			m.quit = true
			return m, m.settle.Finish()
		}
		m.status[msg.i] = stepDone
		next := msg.i + 1
		if next >= len(m.steps) {
			m.quit = true
			return m, m.settle.Finish()
		}
		m.status[next] = stepRunning
		return m, m.run(next)
	case spinner.TickMsg:
		if m.quit && !m.aborting {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			m.err = tea.ErrInterrupted // mapped to a friendly "Cancelled" by callers
			m.quit = true
			// A step's Run can't be stopped mid-flight; wait for its result so
			// the program never exits with work (and file writes) still going.
			for _, st := range m.status {
				if st == stepRunning {
					m.aborting = true
					return m, nil
				}
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *stepsModel) View() tea.View {
	var b []byte
	for i, s := range m.steps {
		switch m.status[i] {
		case stepDone:
			b = append(b, []byte(okStyle.Render("✓ ")+s.Label+"\n")...)
		case stepRunning:
			b = append(b, []byte(m.spinner.View()+" "+s.Label+"\n")...)
		case stepFailed:
			b = append(b, []byte(errStyle.Render("✗ ")+s.Label+"\n")...)
		default:
			b = append(b, []byte(mutedStyle.Render("○ "+s.Label)+"\n")...)
		}
	}
	return tea.NewView(string(b))
}
