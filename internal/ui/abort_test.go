package ui

import (
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/driverforge/cli/internal/clierr"
)

func TestExpectedIfAborted(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantUser bool
	}{
		{"nil", nil, false},
		{"huh abort", huh.ErrUserAborted, true},
		{"wrapped huh abort", fmt.Errorf("form: %w", huh.ErrUserAborted), true},
		{"tea killed", tea.ErrProgramKilled, true},
		{"tea interrupted", tea.ErrInterrupted, true},
		{"other error", errors.New("disk full"), false},
	}
	for _, c := range cases {
		got := ExpectedIfAborted(c.err)
		if c.err == nil {
			if got != nil {
				t.Errorf("%s: got %v, want nil", c.name, got)
			}
			continue
		}
		if clierr.IsUser(got) != c.wantUser {
			t.Errorf("%s: IsUser = %v, want %v (err=%v)", c.name, clierr.IsUser(got), c.wantUser, got)
		}
	}
}

func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl} }

// Every interactive model must resolve a ctrl+c into an error that
// ExpectedIfAborted maps to a friendly UserError — a plain error here renders
// the crash card and offers a Sentry report for a routine user cancel.
func TestCtrlCMapsToFriendlyCancel(t *testing.T) {
	cases := []struct {
		name string
		// press sends ctrl+c to the model and returns its error and quit cmd.
		press func() (error, tea.Cmd)
	}{
		{"FrameModel", func() (error, tea.Cmd) {
			m := NewFrameModel("Building…", false)
			_, cmd := m.Update(ctrlC())
			return m.Err(), cmd
		}},
		{"DeployModel", func() (error, tea.Cmd) {
			m := NewDeployModel("Deploying test to", "controller")
			_, cmd := m.Update(ctrlC())
			return m.Err(), cmd
		}},
		{"stepsModel", func() (error, tea.Cmd) {
			m := &stepsModel{steps: []Step{{Label: "x", Run: func() error { return nil }}},
				status: make([]stepState, 1), spinner: newSpinner()}
			_, cmd := m.Update(ctrlC())
			// RunSteps returns ExpectedIfAborted(m.err); reproduce that seam.
			return m.err, cmd
		}},
		{"stepsModel with in-flight step", func() (error, tea.Cmd) {
			m := &stepsModel{steps: []Step{{Label: "x", Run: func() error { return nil }}},
				status: []stepState{stepRunning}, spinner: newSpinner()}
			if _, cmd := m.Update(ctrlC()); cmd != nil {
				return m.err, nil // must wait for the step, not quit
			}
			// The in-flight step's result arrives, then the model quits.
			_, cmd := m.Update(stepDoneMsg{i: 0})
			if m.status[0] != stepDone {
				return errors.New("in-flight step outcome not recorded"), cmd
			}
			return m.err, cmd
		}},
		{"stepsModel with a failing in-flight step", func() (error, tea.Cmd) {
			// The step fails as the user cancels: the list records the ✗, but the
			// interrupt still wins — a failed step must not turn a cancel into a
			// reportable build error.
			m := &stepsModel{steps: []Step{{Label: "x", Run: func() error { return nil }}},
				status: []stepState{stepRunning}, spinner: newSpinner()}
			m.Update(ctrlC())
			_, cmd := m.Update(stepDoneMsg{i: 0, err: errors.New("disk full")})
			if m.status[0] != stepFailed {
				return errors.New("failed step outcome not recorded"), cmd
			}
			return m.err, cmd
		}},
		{"waitModel", func() (error, tea.Cmd) {
			m := &waitModel{label: "Connecting…", spinner: newSpinner()}
			_, cmd := m.Update(ctrlC())
			return m.err, cmd // waitFor returns ExpectedIfAborted(m.err)
		}},
		{"VersionModel", func() (error, tea.Cmd) {
			// A pending row keeps the view alive, so ctrl+c is the only way out.
			m := NewVersionView([]VersionGroup{{ID: "cli", Name: "Driverforge",
				Rows: []VersionRow{{Label: "CLI", Pending: true}}}})
			_, cmd := m.Update(ctrlC())
			return m.Err(), cmd
		}},
		{"UpgradePickerModel", func() (error, tea.Cmd) {
			m := NewUpgradePicker(upgradeRows(), nil)
			_, cmd := m.Update(ctrlC())
			return m.Err(), cmd
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err, cmd := tc.press()
			if cmd == nil {
				t.Fatal("ctrl+c did not quit the program")
			}
			if !errors.Is(err, tea.ErrInterrupted) {
				t.Fatalf("model error = %v, want tea.ErrInterrupted", err)
			}
			mapped := ExpectedIfAborted(err)
			var ue *clierr.UserError
			if !errors.As(mapped, &ue) {
				t.Fatalf("ExpectedIfAborted(%v) = %v, want a clierr.UserError", err, mapped)
			}
			if ue.Title != "Cancelled" {
				t.Fatalf("mapped title = %q, want Cancelled", ue.Title)
			}
		})
	}
}

// The pickers use the other cancel contract: instead of an error sentinel they
// quit with an abort flag and let the caller decide. The guard is that ctrl+c
// both quits AND sets that flag — a ctrl+c that quit without it would read as a
// completed picker, and the caller would act on an empty selection rather than
// taking its cancel branch.
func TestCtrlCAbortsPickersWithoutSelecting(t *testing.T) {
	cases := []struct {
		name string
		// press sends ctrl+c and reports (aborted, selected, quit).
		press func() (bool, string, bool)
	}{
		{"DevicePickerModel", func() (bool, string, bool) {
			m := NewDevicePicker("Pick a controller", []DeviceItem{
				{ID: "dev-1", Name: "Theatre EA-5"},
				{ID: "dev-2", Name: "Study EA-1"},
			}, "dev-1")
			_, cmd := m.Update(ctrlC())
			return m.Aborted(), m.Chosen(), cmd != nil
		}},
		{"TreePickerModel", func() (bool, string, bool) {
			m := NewTreePicker("Pick a location", []TreeNode{
				{ID: "home", Name: "Home", Children: []TreeNode{
					{ID: "theatre", Name: "Theatre", Selectable: true},
				}},
			})
			_, cmd := m.Update(ctrlC())
			return m.Aborted(), m.Chosen(), cmd != nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			aborted, chosen, quit := tc.press()
			if !quit {
				t.Fatal("ctrl+c did not quit the program")
			}
			if !aborted {
				t.Fatal("ctrl+c quit without setting the abort flag; the caller would read it as a completed picker")
			}
			if chosen != "" {
				t.Fatalf("an aborted picker reported a selection %q", chosen)
			}
		})
	}
}

// reviewModel has no abort flag at all — its caller reads `approved`. A ctrl+c
// must therefore leave it unapproved, so init cancels rather than applying the
// changes the user just backed out of.
func TestCtrlCLeavesReviewUnapproved(t *testing.T) {
	m := newReviewModel([]ReviewFile{{Path: "driver.lua", Old: []byte("a\n"), New: []byte("b\n")}})
	_, cmd := m.Update(ctrlC())
	if cmd == nil {
		t.Fatal("ctrl+c did not quit the program")
	}
	if m.approved {
		t.Fatal("ctrl+c approved the review; init would apply changes the user cancelled")
	}
}
