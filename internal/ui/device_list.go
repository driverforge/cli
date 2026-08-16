package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// padRight pads s with spaces to w display columns (a no-op if it's already at
// least that wide). Measured by lipgloss.Width so non-ASCII/wide runes align.
func padRight(s string, w int) string {
	if pad := w - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// DeviceItem is one row in the live device list / picker.
type DeviceItem struct {
	ID      string // graph device id (returned by the picker on selection)
	Name    string
	Host    string // address from the graph (may be stale)
	Status  string // graph status, e.g. ACTIVE
	MAC     string // normalized (bare lowercase hex) for matching SSDP hits
	Current bool   // the selected/remembered device
}

// FilterValue implements list.Item (the picker filters on name + host).
func (d DeviceItem) FilterValue() string { return d.Name + " " + d.Host }

// DeviceOnlineMsg marks a device (by normalized MAC) as seen live on the network
// via SSDP, with the fresh address from the advertisement (same-segment only).
type DeviceOnlineMsg struct {
	MAC string
	IP  string
}

// DeviceReachableMsg marks a device whose agent answered a direct probe at its
// stored address — the cross-VLAN signal SSDP can't provide.
type DeviceReachableMsg struct {
	MAC string
}

// DeviceScanDoneMsg signals the discovery window closed.
type DeviceScanDoneMsg struct{}

// DeviceListModel renders devices from the graph with a per-row presence dot that
// starts grey and turns green the moment the controller is seen on the network.
type DeviceListModel struct {
	title     string
	items     []DeviceItem
	online    map[string]string // normalized MAC -> live IP (SSDP, same-segment)
	reachable map[string]bool   // normalized MAC -> agent answered a direct probe
	settle    probeSettle       // defers the quit until the terminal's probe replies land
	done      bool
}

// NewDeviceListModel builds the model over the graph's device rows.
func NewDeviceListModel(title string, items []DeviceItem) *DeviceListModel {
	return &DeviceListModel{
		title:     title,
		items:     items,
		online:    map[string]string{},
		reachable: map[string]bool{},
	}
}

func (m *DeviceListModel) Init() tea.Cmd { return nil }

func (m *DeviceListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if handled, cmd := m.settle.Observe(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case DeviceOnlineMsg:
		if msg.MAC != "" {
			m.online[msg.MAC] = msg.IP
		}
		return m, m.settleIfAllOnline()
	case DeviceReachableMsg:
		if msg.MAC != "" {
			m.reachable[msg.MAC] = true
		}
		return m, m.settleIfAllOnline()
	case DeviceScanDoneMsg:
		m.done = true
		return m, m.settle.Finish()
	case tea.KeyPressMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.done = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// settleIfAllOnline stops the scan early once nothing is left to discover: every
// device that could light up (has a MAC) is green. Devices with no MAC can never
// be matched, so they don't hold the window open.
func (m *DeviceListModel) settleIfAllOnline() tea.Cmd {
	for _, it := range m.items {
		if it.MAC == "" {
			continue
		}
		if _, ok := m.online[it.MAC]; ok {
			continue
		}
		if m.reachable[it.MAC] {
			continue
		}
		return nil // a matchable device is still grey — keep scanning
	}
	m.done = true
	// A fast network can settle this instantly — before the terminal answers
	// Bubble Tea's startup probes — so the quit goes through the settle guard.
	return m.settle.Finish()
}

func (m *DeviceListModel) View() tea.View {
	type row struct {
		mark, name, host, status string
		online                   bool
	}
	rows := make([]row, len(m.items))
	nameW, hostW := lipgloss.Width("NAME"), lipgloss.Width("HOST")
	online := 0
	for i, it := range m.items {
		host := it.Host
		live := false
		if it.MAC != "" {
			if ip, ok := m.online[it.MAC]; ok { // SSDP — same segment, fresh address
				live = true
				if ip != "" {
					host = ip
				}
			} else if m.reachable[it.MAC] { // direct probe — works cross-VLAN
				live = true
			}
		}
		if live {
			online++
		}
		mark := " "
		if it.Current {
			mark = "*"
		}
		rows[i] = row{mark: mark, name: it.Name, host: host, status: it.Status, online: live}
		if w := lipgloss.Width(it.Name); w > nameW {
			nameW = w
		}
		if w := lipgloss.Width(host); w > hostW {
			hostW = w
		}
	}

	var b strings.Builder
	b.WriteString(titleStyle.Render(m.title) + "\n\n")
	// 4 leading columns of fixed width: dot(1) space mark(1) space. Pad by
	// display width (not bytes) so non-ASCII names keep the columns aligned.
	fmt.Fprintf(&b, "    %s  %s  %s\n", padRight("NAME", nameW), padRight("HOST", hostW), "STATUS")
	for _, r := range rows {
		dot := mutedStyle.Render("●")
		if r.online {
			dot = okStyle.Render("●")
		}
		fmt.Fprintf(&b, "%s %s %s  %s  %s\n", dot, r.mark, padRight(r.name, nameW), padRight(r.host, hostW), r.status)
	}
	if m.done {
		b.WriteString("\n" + mutedStyle.Render(fmt.Sprintf("%d of %d online", online, len(m.items))))
	} else {
		b.WriteString("\n" + mutedStyle.Render("scanning the network… (q to stop)"))
	}
	return tea.NewView(b.String())
}
