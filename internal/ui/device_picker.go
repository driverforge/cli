package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// DevicePickerModel is a live single-select over devices, rendered in the same
// visual grammar as the huh form selects (see form_theme.go): a brand-accent
// left bar spanning the title and the choices, a ❯ caret on the cursor row,
// and a muted help line — so `driverforge device select` reads exactly like
// `driverforge org select`. Each device is a two-line row (name, then address)
// with a presence dot that flips green as SSDP/probe results stream in.
type DevicePickerModel struct {
	title     string
	items     []DeviceItem
	cursor    int
	online    map[string]string // normalized MAC -> live IP (SSDP, same-segment)
	reachable map[string]bool   // normalized MAC -> agent answered a direct probe
	chosen    string
	aborted   bool
}

// NewDevicePicker builds the picker, preselecting the item whose ID is defID.
func NewDevicePicker(title string, items []DeviceItem, defID string) *DevicePickerModel {
	m := &DevicePickerModel{
		title:     title,
		items:     items,
		online:    map[string]string{},
		reachable: map[string]bool{},
	}
	for i, it := range items {
		if it.ID == defID {
			m.cursor = i
		}
	}
	return m
}

func (m *DevicePickerModel) Init() tea.Cmd { return nil }

func (m *DevicePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case DeviceOnlineMsg:
		if msg.MAC != "" {
			m.online[msg.MAC] = msg.IP
		}
		return m, nil
	case DeviceReachableMsg:
		if msg.MAC != "" {
			m.reachable[msg.MAC] = true
		}
		return m, nil
	case DeviceScanDoneMsg:
		return m, nil
	case tea.KeyPressMsg:
		switch msg.String() {
		case "up", "k":
			m.cursor = (m.cursor - 1 + len(m.items)) % len(m.items)
		case "down", "j":
			m.cursor = (m.cursor + 1) % len(m.items)
		case "enter":
			if len(m.items) > 0 {
				m.chosen = m.items[m.cursor].ID
			}
			return m, tea.Quit
		case "q", "esc", "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *DevicePickerModel) View() tea.View {
	bar := lipgloss.NewStyle().Foreground(colInfo).Render("┃ ")
	var b strings.Builder
	b.WriteString("\n") // match the form theme's top margin
	b.WriteString(bar + titleStyle.Render(m.title) + "\n")
	b.WriteString(bar + "\n")

	for i, it := range m.items {
		host := it.Host
		online := false
		if it.MAC != "" {
			if ip, ok := m.online[it.MAC]; ok {
				online = true
				if ip != "" {
					host = ip
				}
			} else if m.reachable[it.MAC] {
				online = true
			}
		}
		dot := mutedStyle.Render("●")
		if online {
			dot = okStyle.Render("●")
		}

		name := it.Name
		if it.Current {
			name += " (current)"
		}

		caret := "  "
		nameStyle := lipgloss.NewStyle().Foreground(formNormal)
		hostStyle := mutedStyle
		if i == m.cursor {
			caret = lipgloss.NewStyle().Foreground(colInfoBright).Render("❯ ")
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(colInfoBright)
			hostStyle = lipgloss.NewStyle().Foreground(colInfo)
		}

		b.WriteString(bar + caret + dot + " " + nameStyle.Render(name) + "\n")
		b.WriteString(bar + "    " + hostStyle.Render(host) + "\n")
	}

	b.WriteString("\n" + mutedStyle.Render("↑ up • ↓ down • enter submit • q cancel"))
	return tea.NewView(b.String())
}

// Chosen returns the selected device id (empty if none/aborted).
func (m *DevicePickerModel) Chosen() string { return m.chosen }

// Aborted reports whether the user cancelled the picker.
func (m *DevicePickerModel) Aborted() bool { return m.aborted }
