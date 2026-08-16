package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TreeNode is one node in a hierarchical picker — e.g. a location in a Control4
// project (site → building → floor → room). Children make it a branch; a node is
// only choosable as a result when Selectable is true (the agent marks which
// locations a driver may actually be installed into — typically rooms — leaving
// the rest as navigation-only containers).
type TreeNode struct {
	ID         string
	Name       string
	Type       string // display hint, e.g. "room" (drives the icon); optional
	Selectable bool
	Children   []TreeNode
}

// treeRow is a flattened, currently-visible node: the tree collapsed down to the
// linear list the cursor walks. Recomputed whenever the expansion set changes.
type treeRow struct {
	node     *TreeNode
	depth    int
	expanded bool
}

// TreePickerModel is a hand-rolled single-select over a node hierarchy. bubbles
// has no tree widget, so we render it ourselves: indentation per depth, an
// expand caret on branches, and the same selection bar/colours as the device
// picker. ←/→ collapse/expand, ↑/↓ move, enter chooses a selectable node.
type TreePickerModel struct {
	roots    []TreeNode
	expanded map[string]bool
	rows     []treeRow
	cursor   int
	title    string
	chosenID string
	aborted  bool
	height   int // terminal rows, 0 until the first WindowSizeMsg
}

// NewTreePicker builds a picker over roots. Every branch starts expanded (project
// location trees are small and full visibility is the most discoverable), and the
// cursor lands on the first selectable node so enter is immediately useful.
func NewTreePicker(title string, roots []TreeNode) *TreePickerModel {
	m := &TreePickerModel{
		roots:    roots,
		expanded: map[string]bool{},
		title:    title,
	}
	m.forEach(func(n *TreeNode, _ int) {
		if len(n.Children) > 0 {
			m.expanded[n.ID] = true
		}
	})
	m.rebuild()
	for i, r := range m.rows {
		if r.node.Selectable {
			m.cursor = i
			break
		}
	}
	return m
}

// forEach walks every node depth-first (regardless of expansion).
func (m *TreePickerModel) forEach(fn func(n *TreeNode, depth int)) {
	var walk func(ns []TreeNode, depth int)
	walk = func(ns []TreeNode, depth int) {
		for i := range ns {
			n := &ns[i]
			fn(n, depth)
			walk(n.Children, depth+1)
		}
	}
	walk(m.roots, 0)
}

// rebuild recomputes the visible rows from the current expansion set.
func (m *TreePickerModel) rebuild() {
	m.rows = m.rows[:0]
	var walk func(ns []TreeNode, depth int)
	walk = func(ns []TreeNode, depth int) {
		for i := range ns {
			n := &ns[i]
			exp := m.expanded[n.ID]
			m.rows = append(m.rows, treeRow{node: n, depth: depth, expanded: exp})
			if exp {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(m.roots, 0)
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m *TreePickerModel) Init() tea.Cmd { return nil }

func (m *TreePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		m.height = ws.Height
		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "up", "k":
		m.move(-1)
	case "down", "j":
		m.move(1)
	case "right", "l":
		m.expand()
	case "left", "h":
		m.collapseOrParent()
	case "enter", "space":
		m.choose()
		if m.chosenID != "" {
			return m, tea.Quit
		}
	case "q", "esc", "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	}
	return m, nil
}

func (m *TreePickerModel) move(d int) {
	if len(m.rows) == 0 {
		return
	}
	m.cursor = (m.cursor + d + len(m.rows)) % len(m.rows)
}

func (m *TreePickerModel) cur() *treeRow {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return &m.rows[m.cursor]
}

func (m *TreePickerModel) expand() {
	r := m.cur()
	if r == nil || len(r.node.Children) == 0 {
		return
	}
	m.expanded[r.node.ID] = true
	m.rebuild()
}

// collapseOrParent collapses an open branch, or—if the node is already
// collapsed/a leaf—jumps the cursor to its parent, the familiar tree gesture.
func (m *TreePickerModel) collapseOrParent() {
	r := m.cur()
	if r == nil {
		return
	}
	if len(r.node.Children) > 0 && m.expanded[r.node.ID] {
		m.expanded[r.node.ID] = false
		m.rebuild()
		return
	}
	for i := m.cursor - 1; i >= 0; i-- {
		if m.rows[i].depth < r.depth {
			m.cursor = i
			return
		}
	}
}

// choose selects the node under the cursor when it is a valid location; on a
// non-selectable branch enter toggles expansion instead, so it is never a dead key.
func (m *TreePickerModel) choose() {
	r := m.cur()
	if r == nil {
		return
	}
	if r.node.Selectable {
		m.chosenID = r.node.ID
		return
	}
	if len(r.node.Children) > 0 {
		m.expanded[r.node.ID] = !m.expanded[r.node.ID]
		m.rebuild()
	}
}

func (m *TreePickerModel) View() tea.View {
	var b strings.Builder
	if m.title != "" {
		b.WriteString(titleStyle.Render(m.title))
		b.WriteString("\n\n")
	}
	if len(m.rows) == 0 {
		b.WriteString(mutedStyle.Render("  (no locations)"))
		return tea.NewView(b.String())
	}

	// Window the rows to the terminal height so the title, cursor, and help line
	// stay on screen when the tree is taller than the window (Bubble Tea drops
	// leading lines otherwise). start..end is a scrolling slice around the cursor.
	start, end := m.visibleRange()
	if start > 0 {
		b.WriteString(mutedStyle.Render("  ⋯ more above") + "\n")
	}
	for i := start; i < end; i++ {
		r := m.rows[i]
		selected := i == m.cursor
		bar := "  "
		nameStyle := lipgloss.NewStyle().Foreground(colInfoBright)
		if selected {
			bar = lipgloss.NewStyle().Foreground(colInfo).Render("┃ ")
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(colInfoBright)
		}
		if !r.node.Selectable {
			// Containers read as structure, not choices.
			nameStyle = mutedStyle
			if selected {
				nameStyle = lipgloss.NewStyle().Foreground(colInfo)
			}
		}

		indent := strings.Repeat("  ", r.depth)
		caret := "  "
		if len(r.node.Children) > 0 {
			if r.expanded {
				caret = "▾ "
			} else {
				caret = "▸ "
			}
		}
		caret = mutedStyle.Render(caret)

		b.WriteString(bar)
		b.WriteString(indent)
		b.WriteString(caret)
		b.WriteString(nameStyle.Render(r.node.Name))
		b.WriteByte('\n')
	}
	if end < len(m.rows) {
		b.WriteString(mutedStyle.Render("  ⋯ more below") + "\n")
	}

	b.WriteString("\n")
	b.WriteString(mutedStyle.Render("  ↑/↓ move · →/← expand/collapse · enter select · q cancel"))
	return tea.NewView(b.String())
}

// visibleRange returns the [start,end) slice of rows to render so the list fits
// the terminal height, scrolled to keep the cursor in view. With no known
// height (no WindowSizeMsg yet) it shows everything.
func (m *TreePickerModel) visibleRange() (int, int) {
	// Rows the chrome consumes: title (2) + blank + help (2) + the two "⋯ more"
	// markers. Be conservative so nothing is clipped.
	const chrome = 7
	if m.height <= 0 || len(m.rows) <= m.height-chrome {
		return 0, len(m.rows)
	}
	visible := m.height - chrome
	if visible < 1 {
		visible = 1
	}
	start := m.cursor - visible/2 // centre the cursor
	if start < 0 {
		start = 0
	}
	if start+visible > len(m.rows) {
		start = len(m.rows) - visible
	}
	return start, start + visible
}

// Chosen returns the selected node id (empty if none/aborted).
func (m *TreePickerModel) Chosen() string { return m.chosenID }

// Aborted reports whether the user cancelled the picker.
func (m *TreePickerModel) Aborted() bool { return m.aborted }

// ChosenNode returns the full selected node, or nil. Convenient when the caller
// needs the display name (not just the id) for confirmation messages.
func (m *TreePickerModel) ChosenNode() *TreeNode {
	if m.chosenID == "" {
		return nil
	}
	var found *TreeNode
	m.forEach(func(n *TreeNode, _ int) {
		if n.ID == m.chosenID {
			found = n
		}
	})
	return found
}
