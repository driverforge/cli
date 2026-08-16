package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/selfupdate"
	"github.com/driverforge/cli/internal/ui"
)

func TestStatusCell(t *testing.T) {
	cases := []struct {
		name string
		row  compRow
		want []string // substrings expected in the rendered cell
	}{
		{"up to date", compRow{state: stateUpToDate, note: "up to date"}, []string{"✓", "up to date"}},
		{"update with cmd", compRow{state: stateUpdate, note: "update available", cmd: "driverforge upgrade"}, []string{"⬆", "update available", "→ driverforge upgrade"}},
		{"unknown", compRow{state: stateUnknown, note: "couldn't check — sign-in issue"}, []string{"⚠", "sign-in issue"}},
		{"not applicable", compRow{state: stateNA, note: "no controller selected"}, []string{"–", "no controller selected"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := statusCell(c.row)
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("statusCell = %q, want it to contain %q", got, w)
				}
			}
		})
	}
}

// TestStatusCellNoCmdHint ensures the "→ cmd" hint only appears when a command is set.
func TestStatusCellNoCmdHint(t *testing.T) {
	if got := statusCell(compRow{state: stateUpToDate, note: "up to date"}); strings.Contains(got, "→") {
		t.Errorf("a row without a cmd should have no arrow hint: %q", got)
	}
}

// buildUpgradeRows folds the detail into the note, maps states, and carries
// the notes URL through.
func TestBuildUpgradeRows(t *testing.T) {
	rows := []compRow{
		{name: "driverforge CLI", current: "0.4.1", latest: "0.5.0", note: "update available", state: stateUpdate},
		{name: "Anvil Agent", detail: "core1-000FFF9C4F48", current: "2026072104", latest: "2026072201", note: "update available", state: stateUpdate},
		{name: "Driverforge SDK", current: "0.3.0", latest: "0.5.0", note: "update available", state: stateUpdate,
			notesURL: "https://github.com/driverforge/control4-sdk/releases/tag/v0.5.0"},
		{name: "Anvil Agent", note: "no controller selected", state: stateNA},
	}
	got := buildUpgradeRows(rows)

	if got[1].Note != "update available (core1-000FFF9C4F48)" {
		t.Errorf("agent note = %q, want the controller folded in", got[1].Note)
	}
	if got[0].NotesURL != "" || got[1].NotesURL != "" {
		t.Error("only the SDK row should carry a notes URL")
	}
	if !strings.Contains(got[2].NotesURL, "tag/v0.5.0") {
		t.Errorf("sdk notes URL = %q", got[2].NotesURL)
	}
	wantStates := []ui.UpgradeState{ui.UpgradeAvailable, ui.UpgradeAvailable, ui.UpgradeAvailable, ui.UpgradeNA}
	for i, w := range wantStates {
		if got[i].State != w {
			t.Errorf("row %d state = %v, want %v", i, got[i].State, w)
		}
	}
}

// The plain table composes name + (detail), keeping the agent line identical
// to the pre-picker output.
func TestRenderUpgradeTableComposesDetail(t *testing.T) {
	var buf bytes.Buffer
	renderUpgradeTable(&buf, []compRow{
		{name: "Anvil Agent", detail: "core1-000FFF9C4F48", current: "2026072104", latest: "2026072201", note: "update available", state: stateUpdate},
	})
	if !strings.Contains(buf.String(), "Anvil Agent (core1-000FFF9C4F48)") {
		t.Errorf("table missing composed agent name:\n%s", buf.String())
	}
}

// Only the interactive picker can invoke a row's run, so the non-interactive
// table is the whole output for a piped, CI or no-TTY caller. An
// update-available CLI row must therefore always carry a command — including
// when the CLI can upgrade itself — or that caller is told an update exists and
// given no way to take it.
func TestUpgradeActionAlwaysOffersACommand(t *testing.T) {
	for _, c := range []struct {
		name           string
		target         selfupdate.Target
		err            error
		wantSelfUpdate bool
	}{
		{"binary is replaceable", selfupdate.Target{Path: "/x", Writable: true}, nil, true},
		{"binary is not writable", selfupdate.Target{Path: "/x"}, nil, false},
		{"could not be located", selfupdate.Target{}, errors.New("nope"), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			cmd, selfUpgradable := upgradeAction(c.target, c.err)
			if cmd == "" {
				t.Error("an update-available row must carry a command; a non-interactive caller sees nothing else")
			}
			if selfUpgradable != c.wantSelfUpdate {
				t.Errorf("selfUpgradable = %v, want %v", selfUpgradable, c.wantSelfUpdate)
			}
		})
	}
}
