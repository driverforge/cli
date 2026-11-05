package ui

import (
	"errors"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"

	"github.com/driverforge/cli/internal/clierr"
)

// ExpectedIfAborted maps an interactive abort — Ctrl-C or Esc out of a huh form,
// or a killed/interrupted Bubble Tea program — to an expected clierr.UserError,
// so it renders as a friendly "Aborted." notice and is never reported to Sentry.
// Any other error (or nil) passes through unchanged.
func ExpectedIfAborted(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, huh.ErrUserAborted) ||
		errors.Is(err, tea.ErrProgramKilled) ||
		errors.Is(err, tea.ErrInterrupted) {
		return clierr.WithCode(clierr.WrapT(err, "Cancelled", "No changes were made.", ""), clierr.CodeInterrupted)
	}
	return err
}
