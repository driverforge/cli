package ui

// Success renders a green, check-marked success line for inline status output.
func Success(msg string) string { return okStyle.Render("✓ " + msg) }

// Muted renders dimmed, secondary status text (e.g. "Waiting…").
func Muted(msg string) string { return mutedStyle.Render(msg) }

// Warning renders an amber, ⚠-prefixed advisory line for non-fatal cautions
// (e.g. an insecure auth endpoint) that shouldn't read as a hard error.
func Warning(msg string) string { return warnStyle.Render("⚠ " + msg) }

// StatusOK / StatusWarn / StatusMuted colour a bare status string in the shared
// semantic palette (green / amber / dim) without a leading icon, so callers that
// compose their own cells — e.g. the `driverforge upgrade` table — needn't import
// lipgloss or redefine the theme colours.
func StatusOK(msg string) string    { return okStyle.Render(msg) }
func StatusWarn(msg string) string  { return warnStyle.Render(msg) }
func StatusMuted(msg string) string { return mutedStyle.Render(msg) }

// Skipped echoes a captured "no"/skip selection with a neutral ⏺ dot (the
// Claude Code marker — familiar and on-brand) — so a negative answer reads as
// noted, not failed (a red ✗ would look like an error).
func Skipped(msg string) string { return mutedStyle.Render("⏺ " + msg) }

// Celebrate renders the final success headline for a completed wizard run.
func Celebrate(headline string) string {
	return okStyle.Bold(true).Render("🎉  " + headline)
}

// Heading renders a bold, brand-accented section heading.
func Heading(msg string) string { return titleStyle.Render(msg) }

// Failure renders a red error line for inline status output where the
// process keeps going (a failed callback in a debug session, say) — the
// counterpart of Success/Warning, not a fatal-error banner.
func Failure(msg string) string { return errStyle.Render(msg) }
