package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func versionGroupsFixture() []VersionGroup {
	return []VersionGroup{
		{ID: "driverforge", Name: "Driverforge", Rows: []VersionRow{
			{Label: "CLI", Value: "v0.2.2"},
			{Label: "Graph", Pending: true},
		}},
		{ID: "controller", Name: "core1-000FFF9C4F48", Rows: []VersionRow{
			{Label: "Anvil Agent", Pending: true},
		}},
	}
}

// Static rows render immediately; pending ones spin, resolve in place, and
// the view quits itself once nothing is pending.
func TestVersionViewResolvesPendingRows(t *testing.T) {
	m := NewVersionView(versionGroupsFixture())
	answerProbes(m)

	v := m.View().Content
	if !strings.Contains(v, "v0.2.2") || !strings.Contains(v, "Driverforge") {
		t.Errorf("static rows should render immediately:\n%s", v)
	}

	_, cmd := m.Update(VersionUpdateMsg{ID: "driverforge", Rows: []VersionRow{
		{Label: "CLI", Value: "v0.2.2"},
		{Label: "Graph", Value: "sha-abc1234"},
	}})
	if cmd != nil {
		t.Fatal("must not quit while the controller row is still pending")
	}

	_, cmd = m.Update(VersionUpdateMsg{ID: "controller", Name: "Theatre Core", Rows: []VersionRow{
		{Label: "Anvil Agent", Value: "2026072201"},
		{Label: "Director", Value: "4.0.0"},
	}})
	if _, ok := runCmd(cmd).(tea.QuitMsg); !ok {
		t.Fatal("resolving the last pending row should quit")
	}

	v = m.View().Content
	for _, want := range []string{"sha-abc1234", "Theatre Core", "2026072201", "Director"} {
		if !strings.Contains(v, want) {
			t.Errorf("resolved view missing %q:\n%s", want, v)
		}
	}
}

// A view with nothing pending quits straight from Init (after the settle
// guard).
func TestVersionViewStaticOnlyQuits(t *testing.T) {
	m := NewVersionView([]VersionGroup{{ID: "d", Name: "Driverforge", Rows: []VersionRow{{Label: "CLI", Value: "v1"}}}})
	answerProbes(m)
	if _, ok := runCmd(m.Init()).(tea.BatchMsg); !ok {
		// Init batches the spinner tick with the quit; just assert it returned
		// something rather than hanging forever.
		t.Fatal("Init with nothing pending should schedule the quit")
	}
}

func TestVersionViewInterrupt(t *testing.T) {
	m := NewVersionView(versionGroupsFixture())
	_, cmd := m.Update(ctrlC())
	if cmd == nil {
		t.Fatal("ctrl+c should quit")
	}
	if !errors.Is(m.Err(), tea.ErrInterrupted) {
		t.Fatalf("Err() = %v, want tea.ErrInterrupted", m.Err())
	}
}
