package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// UpgradeState classifies a component row for the upgrade picker. Only
// UpgradeAvailable rows are selectable; the rest render dimmed as status
// context.
type UpgradeState int

const (
	UpgradeAvailable UpgradeState = iota // ⬆ update available — selectable
	UpgradeCurrent                       // ✓ up to date
	UpgradeUnknown                       // ⚠ couldn't check
	UpgradeNA                            // – not applicable
)

// UpgradeRow is one component in the upgrade picker, fully composed by the
// caller (Note carries any parenthetical detail, e.g. the controller name).
// A non-empty NotesURL enables the release-notes action for the row.
type UpgradeRow struct {
	Name     string
	Current  string
	Latest   string
	Note     string
	State    UpgradeState
	NotesURL string
}

func (r UpgradeRow) selectable() bool { return r.State == UpgradeAvailable }

// upgradeNotesOpenedMsg reports the outcome of opening a row's release notes
// in the browser (a side effect, so it runs as a command).
type upgradeNotesOpenedMsg struct {
	url string
	err error
}

// UpgradePickerModel is the interactive component list `driverforge upgrade`
// shows in place of the plain status table: every component with its
// current → latest versions and status, updatable rows as toggleable
// checkboxes, plus actions to apply the selection and view release notes.
type UpgradePickerModel struct {
	rows      []UpgradeRow
	selected  []bool
	cursor    int                // always on a selectable row
	openURL   func(string) error // browser opener; nil shows the URL only
	flash     string             // transient footer feedback, cleared on the next key
	confirmed bool
	aborted   bool
	err       error
}

// NewUpgradePicker builds the picker. Every row starts DESELECTED — an upgrade
// (especially the agent, which restarts and may replace a custom build) must be
// an explicit choice, never a muscle-memory enter. The cursor lands on the
// first updatable row. openURL opens a release-notes link in the user's
// browser (the caller injects it to keep this package UI-only).
func NewUpgradePicker(rows []UpgradeRow, openURL func(string) error) *UpgradePickerModel {
	m := &UpgradePickerModel{rows: rows, selected: make([]bool, len(rows)), openURL: openURL}
	for i, r := range rows {
		if r.selectable() {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *UpgradePickerModel) Init() tea.Cmd { return nil }

func (m *UpgradePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case upgradeNotesOpenedMsg:
		if msg.err != nil {
			// No opener (headless/SSH) — show the link itself; linkify makes it
			// clickable in terminals that support OSC-8.
			m.flash = mutedStyle.Render("Release notes: ") + linkify(msg.url)
		} else {
			m.flash = Muted("Opened release notes in your browser")
		}
		return m, nil
	case tea.KeyPressMsg:
		m.flash = ""
		switch msg.String() {
		case "up", "k":
			m.move(-1)
		case "down", "j":
			m.move(1)
		case "space":
			if m.rows[m.cursor].selectable() {
				m.selected[m.cursor] = !m.selected[m.cursor]
			}
		case "enter":
			m.confirmed = true
			return m, tea.Quit
		case "r":
			return m, m.openNotes()
		case "q", "esc":
			m.aborted = true
			return m, tea.Quit
		case "ctrl+c":
			// The sentinel makes ExpectedIfAborted map this to a friendly
			// "Cancelled" card instead of a reportable crash.
			m.err = tea.ErrInterrupted
			m.aborted = true
			return m, tea.Quit
		}
	}
	return m, nil
}

// move advances the cursor to the next selectable row in direction d, wrapping.
func (m *UpgradePickerModel) move(d int) {
	if len(m.rows) == 0 {
		return
	}
	i := m.cursor
	for range m.rows {
		i = (i + d + len(m.rows)) % len(m.rows)
		if m.rows[i].selectable() {
			m.cursor = i
			return
		}
	}
}

// openNotes opens the cursor row's release notes in the browser as a command
// (a side effect doesn't belong in Update). Rows without notes just flash.
func (m *UpgradePickerModel) openNotes() tea.Cmd {
	row := m.rows[m.cursor]
	if row.NotesURL == "" {
		m.flash = Muted("No release notes for " + row.Name + ".")
		return nil
	}
	open, url := m.openURL, row.NotesURL
	if open == nil {
		m.flash = mutedStyle.Render("Release notes: ") + linkify(url)
		return nil
	}
	return func() tea.Msg { return upgradeNotesOpenedMsg{url: url, err: open(url)} }
}

func (m *UpgradePickerModel) View() tea.View {
	// Column widths from the unstyled cells, so ANSI codes never skew padding.
	nameW, verW := 0, 0
	for _, r := range m.rows {
		if len(r.Name) > nameW {
			nameW = len(r.Name)
		}
		if v := len(m.versionCell(r)); v > verW {
			verW = v
		}
	}

	var b strings.Builder
	for i, r := range m.rows {
		b.WriteString(m.renderRow(i, r, nameW, verW) + "\n")
	}
	b.WriteString("\n")
	if m.flash != "" {
		b.WriteString("  " + m.flash + "\n")
	}
	b.WriteString(mutedStyle.Render("  space select · enter upgrade selected · r release notes · q quit"))
	return tea.NewView(b.String())
}

// versionCell is the unstyled version column: "current → latest" for an
// updatable row, just the current version otherwise.
func (m *UpgradePickerModel) versionCell(r UpgradeRow) string {
	if r.State == UpgradeAvailable {
		return r.Current + " → " + r.Latest
	}
	return r.Current
}

func (m *UpgradePickerModel) renderRow(i int, r UpgradeRow, nameW, verW int) string {
	name := fmt.Sprintf("%-*s", nameW, r.Name)
	ver := fmt.Sprintf("%-*s", verW, m.versionCell(r))

	if !r.selectable() {
		var icon string
		switch r.State {
		case UpgradeCurrent:
			icon = "✓"
		case UpgradeUnknown:
			icon = "⚠"
		default:
			icon = "–"
		}
		return mutedStyle.Render(fmt.Sprintf("  %s %s   %s   %s", icon, name, ver, r.Note))
	}

	bar := "  "
	nameStyle := valStyle
	if i == m.cursor {
		bar = lipgloss.NewStyle().Foreground(colInfo).Render("❯ ")
		nameStyle = lipgloss.NewStyle().Bold(true).Foreground(colInfoBright)
	}
	marker := valStyle.Render("◯")
	if m.selected[i] {
		marker = okStyle.Render("◉")
	}
	return bar + marker + " " + nameStyle.Render(name) + "   " + valStyle.Render(ver) + "   " + warnStyle.Render("⬆ "+r.Note)
}

// Chosen returns the indices of the rows the user confirmed for upgrade
// (nil unless enter was pressed).
func (m *UpgradePickerModel) Chosen() []int {
	if !m.confirmed {
		return nil
	}
	var out []int
	for i, sel := range m.selected {
		if sel {
			out = append(out, i)
		}
	}
	return out
}

// Aborted reports whether the user backed out without confirming.
func (m *UpgradePickerModel) Aborted() bool { return m.aborted }

// Err returns the interrupt sentinel after a ctrl+c (nil otherwise).
func (m *UpgradePickerModel) Err() error { return m.err }
