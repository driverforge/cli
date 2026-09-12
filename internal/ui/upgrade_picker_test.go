package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func upgradeRows() []UpgradeRow {
	return []UpgradeRow{
		{Name: "driverforge CLI", Current: "0.4.1", Latest: "0.5.0", Note: "update available", State: UpgradeAvailable},
		{Name: "Anvil Agent", Current: "2026072104", Note: "up to date", State: UpgradeCurrent},
		{Name: "Driverforge SDK", Current: "0.3.0", Latest: "0.5.0", Note: "update available", State: UpgradeAvailable,
			NotesURL: "https://github.com/driverforge/control4-sdk/releases/tag/v0.5.0"},
	}
}

// Updatable rows start selected with the cursor on the first one; dimmed rows
// are skipped by cursor movement, which wraps.
func TestUpgradePickerCursorSkipsDimmedRows(t *testing.T) {
	m := NewUpgradePicker(upgradeRows(), nil)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m.Update(key("j"))
	if m.cursor != 2 {
		t.Fatalf("cursor after j = %d, want 2 (skipping the dimmed row)", m.cursor)
	}
	m.Update(key("j"))
	if m.cursor != 0 {
		t.Fatalf("cursor after wrap = %d, want 0", m.cursor)
	}
	m.Update(key("k"))
	if m.cursor != 2 {
		t.Fatalf("cursor after k = %d, want 2", m.cursor)
	}
}

// Rows start DESELECTED — an upgrade must be an explicit choice, so a
// muscle-memory enter applies nothing.
func TestUpgradePickerStartsDeselected(t *testing.T) {
	m := NewUpgradePicker(upgradeRows(), nil)
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Aborted() {
		t.Fatal("enter is a confirm, not an abort")
	}
	if got := m.Chosen(); len(got) != 0 {
		t.Fatalf("Chosen() with no selection = %v, want empty", got)
	}
}

func TestUpgradePickerToggleAndConfirm(t *testing.T) {
	m := NewUpgradePicker(upgradeRows(), nil)
	m.Update(key("j"))                            // cursor → SDK row
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace}) // select it
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should quit")
	}
	got := m.Chosen()
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("Chosen() = %v, want [2]", got)
	}
}

func TestUpgradePickerQuitAndInterrupt(t *testing.T) {
	m := NewUpgradePicker(upgradeRows(), nil)
	_, cmd := m.Update(key("q"))
	if cmd == nil || !m.Aborted() {
		t.Fatal("q should abort and quit")
	}
	if m.Chosen() != nil {
		t.Fatal("an aborted picker must not report a selection")
	}

	m = NewUpgradePicker(upgradeRows(), nil)
	_, cmd = m.Update(ctrlC())
	if cmd == nil {
		t.Fatal("ctrl+c should quit")
	}
	if !errors.Is(m.Err(), tea.ErrInterrupted) {
		t.Fatalf("Err() = %v, want tea.ErrInterrupted", m.Err())
	}
}

// r on a row without notes flashes a hint; r on the SDK row opens the URL in
// the browser (or shows the link when the opener fails). The flash clears on
// the next keypress.
func TestUpgradePickerReleaseNotes(t *testing.T) {
	var opened []string
	open := func(url string) error {
		opened = append(opened, url)
		return nil
	}
	m := NewUpgradePicker(upgradeRows(), open)

	// CLI row: no notes.
	m.Update(key("r"))
	if v := m.View().Content; !strings.Contains(v, "No release notes for driverforge CLI.") {
		t.Errorf("expected no-notes flash:\n%s", v)
	}

	// SDK row: opens the URL via the injected opener.
	m.Update(key("j"))
	_, cmd := m.Update(key("r"))
	if cmd == nil {
		t.Fatal("r on a notes row should return an open command")
	}
	m.Update(cmd())
	if len(opened) != 1 || !strings.Contains(opened[0], "tag/v0.5.0") {
		t.Fatalf("opened = %v, want the v0.5.0 tag URL", opened)
	}
	if v := m.View().Content; !strings.Contains(v, "Opened release notes") {
		t.Errorf("expected opened-in-browser flash:\n%s", v)
	}

	// The flash clears on the next keypress.
	m.Update(key("k"))
	if v := m.View().Content; strings.Contains(v, "Opened release notes") {
		t.Errorf("flash should clear on the next key:\n%s", v)
	}
}

// A failing opener still surfaces the URL so it can be clicked/copied.
func TestUpgradePickerNotesOpenFailureShowsURL(t *testing.T) {
	open := func(string) error { return errors.New("no xdg-open") }
	m := NewUpgradePicker(upgradeRows(), open)
	m.Update(key("j")) // SDK row
	_, cmd := m.Update(key("r"))
	m.Update(cmd())
	if v := m.View().Content; !strings.Contains(v, "tag/v0.5.0") {
		t.Errorf("expected the URL in the flash when the opener fails:\n%s", v)
	}
}

// Dimmed rows render muted with their status; selectable rows carry markers —
// empty (◯) until explicitly selected (◉).
func TestUpgradePickerViewShape(t *testing.T) {
	m := NewUpgradePicker(upgradeRows(), nil)
	v := m.View().Content
	for _, want := range []string{"❯", "◯", "✓", "up to date", "0.3.0 → 0.5.0", "space select"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "◉") {
		t.Errorf("no row should start selected:\n%s", v)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	if v := m.View().Content; !strings.Contains(v, "◉") {
		t.Errorf("space should mark the row selected:\n%s", v)
	}
}

// A blocked row (an SDK update behind a dirty working tree) shows both
// versions and its note but can't be reached or toggled, while the CLI and
// agent rows around it stay independently selectable.
func TestUpgradePickerBlockedRowIsUnselectable(t *testing.T) {
	rows := []UpgradeRow{
		{Name: "driverforge CLI", Current: "0.4.1", Latest: "0.5.0", Note: "update available", State: UpgradeAvailable},
		{Name: "Anvil Agent", Current: "2026072104", Latest: "2026072201", Note: "update available", State: UpgradeAvailable},
		{Name: "Driverforge SDK", Current: "0.3.0", Latest: "0.5.0", Note: "update available — Commit or stash your changes to upgrade the SDK.", State: UpgradeBlocked},
	}
	m := NewUpgradePicker(rows, nil)

	v := m.View().Content
	for _, want := range []string{"0.3.0 → 0.5.0", "Commit or stash your changes to upgrade the SDK."} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}

	// Cursor movement never lands on the blocked row.
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0", m.cursor)
	}
	m.Update(key("j"))
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace}) // select the agent
	if m.cursor != 1 {
		t.Fatalf("cursor after j = %d, want 1", m.cursor)
	}
	m.Update(key("j"))
	if m.cursor != 0 {
		t.Fatalf("cursor after second j = %d, want 0 (wrapping past the blocked SDK row)", m.cursor)
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace}) // select the CLI

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := m.Chosen()
	if len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("Chosen() = %v, want [0 1]: CLI and agent, never the blocked SDK", got)
	}
}

// With only a blocked row there is nothing to land on; the picker must still
// construct and render without panicking.
func TestUpgradePickerOnlyBlockedRow(t *testing.T) {
	m := NewUpgradePicker([]UpgradeRow{
		{Name: "Driverforge SDK", Current: "0.3.0", Latest: "0.5.0", Note: "blocked", State: UpgradeBlocked},
	}, nil)
	m.Update(key("j"))
	m.Update(tea.KeyPressMsg{Code: tea.KeySpace})
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.Chosen(); len(got) != 0 {
		t.Fatalf("Chosen() = %v, want empty", got)
	}
}
