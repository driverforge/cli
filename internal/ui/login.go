package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

// DeviceLoginCard renders the device-authorization instructions as a
// left-bordered block in the info accent. When the server supplies a complete
// verification URL (completeURL, with the code embedded) that is the link shown,
// and the code is displayed only so the user can check it matches the page —
// the page pre-fills it and doesn't take input. Without one, it falls back to
// the bare verifyURL plus the code to enter. The link is clickable where the
// terminal supports OSC-8. In a pipe/CI (non-terminal w) it returns the plain
// text form, so scripts and logs stay clean.
func DeviceLoginCard(w io.Writer, verifyURL, completeURL, userCode string) string {
	link, codeLine := completeURL, "and check it shows this code:"
	if completeURL == "" {
		link, codeLine = verifyURL, "and enter this code:"
	}

	if !isTerminalWriter(w) {
		return fmt.Sprintf("To authorize driverforge, visit:\n\n    %s\n\n%s  %s", link, codeLine, userCode)
	}

	rows := []string{
		blockRow(colInk, false, "To authorize driverforge, open this link in your browser:"),
		"",
		// Not wrapped: the link must stay one piece to copy, and the complete
		// URL with its code is longer than the block's content width.
		lipgloss.NewStyle().Foreground(colInfoBright).Bold(true).Render("    " + linkify(link)),
		"",
		blockRow(colInk, false, codeLine),
		"",
		blockRow(colInfoBright, true, "    "+userCode),
	}
	return calloutBlock(colInfo, rows)
}
