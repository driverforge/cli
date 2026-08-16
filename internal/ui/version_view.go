package ui

import (
	"context"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// VersionRow is one "label  value" line in a version group. A Pending row
// renders a spinner in place of its value until the group is updated.
type VersionRow struct {
	Label   string
	Value   string
	Pending bool
}

// VersionGroup is one owner (Driverforge, the driver, a controller) and the
// versioned things that live with it.
type VersionGroup struct {
	ID   string // update key
	Name string
	Rows []VersionRow
}

// VersionUpdateMsg resolves a group's pending rows: Rows replaces the group's
// row set, and a non-empty Name renames the group (e.g. the controller's
// self-reported name once its health answers).
type VersionUpdateMsg struct {
	ID   string
	Name string
	Rows []VersionRow
}

// VersionModel renders the `driverforge version` report live: static rows
// immediately, network-dependent ones as spinners that resolve in place. The
// program quits itself once nothing is pending.
type VersionModel struct {
	groups  []VersionGroup
	spinner spinner.Model
	settle  probeSettle // defers the quit until the terminal's probe replies land
	err     error
}

// NewVersionView builds the live version report.
func NewVersionView(groups []VersionGroup) *VersionModel {
	return &VersionModel{groups: groups, spinner: newSpinner()}
}

func (m *VersionModel) Init() tea.Cmd {
	if !m.pending() {
		// Nothing to resolve — render once and leave (after the settle guard).
		return tea.Batch(m.spinner.Tick, m.settle.Finish())
	}
	return m.spinner.Tick
}

func (m *VersionModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case VersionUpdateMsg:
		for i := range m.groups {
			if m.groups[i].ID == msg.ID {
				m.groups[i].Rows = msg.Rows
				if msg.Name != "" {
					m.groups[i].Name = msg.Name
				}
			}
		}
		if !m.pending() {
			return m, m.settle.Finish()
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			// The sentinel makes ExpectedIfAborted map this to a friendly
			// "Cancelled" card instead of a reportable crash.
			m.err = tea.ErrInterrupted
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *VersionModel) pending() bool {
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if r.Pending {
				return true
			}
		}
	}
	return false
}

func (m *VersionModel) View() tea.View {
	// One label column across every group, so the versions line up globally.
	width := 0
	for _, g := range m.groups {
		for _, r := range g.Rows {
			if len(r.Label) > width {
				width = len(r.Label)
			}
		}
	}
	var b strings.Builder
	for i, g := range m.groups {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(g.Name + "\n")
		for _, r := range g.Rows {
			value := r.Value
			if r.Pending {
				value = m.spinner.View()
			}
			b.WriteString(fmt.Sprintf("  %-*s  %s\n", width, r.Label, value))
		}
	}
	return tea.NewView(b.String())
}

// Err returns the interrupt sentinel after a ctrl+c (nil otherwise).
func (m *VersionModel) Err() error { return m.err }

// RunVersionView runs the live report: start launches the resolution work
// (posting VersionUpdateMsg values through send) and the program exits once
// every pending row has resolved.
func RunVersionView(ctx context.Context, groups []VersionGroup, start func(send func(VersionUpdateMsg))) error {
	m := NewVersionView(groups)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	go start(func(u VersionUpdateMsg) { p.Send(u) })
	if _, err := p.Run(); err != nil {
		return ExpectedIfAborted(err)
	}
	return ExpectedIfAborted(m.err)
}
