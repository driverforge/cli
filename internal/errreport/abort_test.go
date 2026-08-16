package errreport

import (
	"errors"
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/driverforge/cli/internal/ui"
)

// The last link in the cancel chain: a TUI model resolves ctrl+c (or an Esc out
// of a huh form) to an abort sentinel, ui.ExpectedIfAborted maps that to a
// UserError, and Reportable must then refuse to offer a Sentry report for it.
// internal/ui pins the model → sentinel half (it can't import this package);
// this pins sentinel → not-reportable, so a routine user cancel can never
// render a crash card again.
func TestAbortSentinelsAreNotReportable(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{"tea interrupted (ctrl+c in a model)", tea.ErrInterrupted},
		{"tea program killed", tea.ErrProgramKilled},
		{"huh user aborted (esc out of a form)", huh.ErrUserAborted},
		{"wrapped interrupt", fmt.Errorf("build: %w", tea.ErrInterrupted)},
		{"wrapped huh abort", fmt.Errorf("first-deploy wizard: %w", huh.ErrUserAborted)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapped := ui.ExpectedIfAborted(tc.err)
			if Reportable(mapped) {
				t.Fatalf("Reportable(ExpectedIfAborted(%v)) = true; a user cancel would render a crash card and offer a Sentry report", tc.err)
			}
		})
	}
}

// The mapping must stay narrow: a genuine failure that happens to surface from
// a TUI flow is still a bug and must remain reportable, or ExpectedIfAborted
// would become a blanket suppressor.
func TestNonAbortErrorsStayReportable(t *testing.T) {
	for _, err := range []error{
		errors.New("no space left on device"),
		fmt.Errorf("agent: HTTP 500: internal error"),
	} {
		if !Reportable(ui.ExpectedIfAborted(err)) {
			t.Errorf("Reportable(ExpectedIfAborted(%v)) = false, want true", err)
		}
	}
}
