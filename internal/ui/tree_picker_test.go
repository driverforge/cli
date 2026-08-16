package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func sampleTree() []TreeNode {
	return []TreeNode{{
		ID: "site", Name: "Home", Type: "site",
		Children: []TreeNode{{
			ID: "house", Name: "Main House", Type: "building",
			Children: []TreeNode{
				{ID: "ground", Name: "Ground Floor", Type: "floor", Children: []TreeNode{
					{ID: "living", Name: "Living Room", Type: "room", Selectable: true},
					{ID: "kitchen", Name: "Kitchen", Type: "room", Selectable: true},
				}},
				{ID: "first", Name: "First Floor", Type: "floor", Children: []TreeNode{
					{ID: "master", Name: "Master Bedroom", Type: "room", Selectable: true},
				}},
			},
		}},
	}}
}

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

func TestTreePickerCursorStartsOnFirstSelectable(t *testing.T) {
	m := NewTreePicker("Pick a location", sampleTree())
	// All branches expand by default → first selectable row is "Living Room".
	if got := m.cur().node.ID; got != "living" {
		t.Errorf("cursor should start on first selectable node, got %q", got)
	}
}

func TestTreePickerEnterSelects(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Aborted() {
		t.Fatal("enter on a selectable node should not abort")
	}
	if m.Chosen() != "living" {
		t.Errorf("chosen = %q, want living", m.Chosen())
	}
	if cmd == nil {
		t.Error("selecting should quit the program (non-nil cmd)")
	}
	if n := m.ChosenNode(); n == nil || n.Name != "Living Room" {
		t.Errorf("ChosenNode = %+v, want Living Room", n)
	}
}

func TestTreePickerNavigatesAndSelects(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	m.Update(key("j")) // living -> kitchen
	if m.cur().node.ID != "kitchen" {
		t.Fatalf("down should move to kitchen, at %q", m.cur().node.ID)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Chosen() != "kitchen" {
		t.Errorf("chosen = %q, want kitchen", m.Chosen())
	}
}

func TestTreePickerCollapseHidesChildren(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	// Move up to the "Ground Floor" branch (cursor starts on living, its child).
	m.Update(key("h")) // living is a leaf → jump to parent (ground)
	if m.cur().node.ID != "ground" {
		t.Fatalf("left on a leaf should jump to parent, at %q", m.cur().node.ID)
	}
	before := len(m.rows)
	m.Update(key("h")) // collapse ground
	if len(m.rows) >= before {
		t.Errorf("collapsing should hide rows: before %d, after %d", before, len(m.rows))
	}
	for _, r := range m.rows {
		if r.node.ID == "living" {
			t.Error("living should be hidden after collapsing its parent")
		}
	}
	m.Update(key("l")) // re-expand
	found := false
	for _, r := range m.rows {
		if r.node.ID == "living" {
			found = true
		}
	}
	if !found {
		t.Error("expanding should reveal children again")
	}
}

func TestTreePickerEnterOnContainerTogglesNotSelects(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	m.Update(key("h")) // to ground
	m.Update(key("h")) // to house (parent of ground after collapse? ground collapses, cursor stays)
	// Cursor is on a non-selectable container; enter must toggle, never choose.
	id := m.cur().node.ID
	if m.cur().node.Selectable {
		t.Skip("expected a container under cursor")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Chosen() != "" {
		t.Errorf("enter on container %q should not choose anything, got %q", id, m.Chosen())
	}
}

func TestTreePickerAborts(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	m.Update(key("q"))
	if !m.Aborted() || m.Chosen() != "" {
		t.Errorf("q should abort with no choice; aborted=%v chosen=%q", m.Aborted(), m.Chosen())
	}
}

func TestTreePickerViewRendersHierarchy(t *testing.T) {
	m := NewTreePicker("Pick a location", sampleTree())
	out := m.View().Content
	for _, want := range []string{"Pick a location", "Home", "Living Room", "Kitchen", "Master Bedroom"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "▾") {
		t.Errorf("expanded branches should show a caret:\n%s", out)
	}
}

func TestTreePickerEmpty(t *testing.T) {
	m := NewTreePicker("x", nil)
	if m.cur() != nil {
		t.Error("empty tree should have no current row")
	}
	out := m.View().Content
	if !strings.Contains(out, "no locations") {
		t.Errorf("empty tree should say so:\n%s", out)
	}
	// Keys must not panic on an empty tree.
	m.Update(key("j"))
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

// A tree taller than the terminal must window its rows so the cursor stays
// visible and the frame fits — Bubble Tea drops leading lines otherwise.
func TestTreePickerWindowsToHeight(t *testing.T) {
	// Build a flat list of 40 selectable rows under one expanded root.
	var kids []TreeNode
	for i := 0; i < 40; i++ {
		kids = append(kids, TreeNode{ID: string(rune('a'+i%26)) + string(rune('0'+i/26)), Name: "Room", Type: "room", Selectable: true})
	}
	m := NewTreePicker("Pick", []TreeNode{{ID: "root", Name: "Root", Type: "site", Children: kids}})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})

	start, end := m.visibleRange()
	if end-start >= len(m.rows) {
		t.Fatalf("expected a windowed subset, got %d of %d rows", end-start, len(m.rows))
	}
	// With the cursor at the bottom, the window must follow it and reach the end.
	m.cursor = len(m.rows) - 1
	start, end = m.visibleRange()
	if m.cursor < start || m.cursor >= end {
		t.Errorf("cursor %d not within visible window [%d,%d)", m.cursor, start, end)
	}
	if end != len(m.rows) {
		t.Errorf("window should reach the last row when the cursor is there: end=%d len=%d", end, len(m.rows))
	}
	// And at the top, the window starts at 0.
	m.cursor = 0
	if s0, _ := m.visibleRange(); s0 != 0 {
		t.Errorf("window should start at 0 with the cursor at the top, got %d", s0)
	}
}

// With no WindowSizeMsg (unknown height) every row is shown.
func TestTreePickerNoHeightShowsAll(t *testing.T) {
	m := NewTreePicker("x", sampleTree())
	start, end := m.visibleRange()
	if start != 0 || end != len(m.rows) {
		t.Errorf("unknown height should show all rows, got [%d,%d) of %d", start, end, len(m.rows))
	}
}
