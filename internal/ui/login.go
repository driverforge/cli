package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

// DeviceLoginCard renders the device-authorization instructions as a card: the
// verification URL (clickable where the terminal supports OSC-8) and the user
// code boxed and emphasised. In a pipe/CI (non-terminal w) it returns the plain
// text form, so scripts and logs stay clean.
func DeviceLoginCard(w io.Writer, verifyURL, userCode string) string {
	if !isTerminalWriter(w) {
		return fmt.Sprintf("To authorize driverforge, visit:\n\n    %s\n\nand enter the code:  %s", verifyURL, userCode)
	}

	codeBox := lipgloss.NewStyle().
		Bold(true).
		Foreground(colInfoBright).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colInfo).
		Padding(0, 3).
		Render(userCode)

	rows := []string{
		textRow(colInk, false, "To authorize driverforge, open this link in your browser:"),
		"",
		textRow(colInfoBright, true, linkify(verifyURL)),
		"",
		textRow(colInk, false, "and enter this code:"),
		"",
		centerRow(codeBox),
	}
	return card(colInfo, "Sign in", rows)
}

// centerRow centres pre-rendered content within a card-width row, padding it to
// the same display width as textRow so the card borders stay aligned.
func centerRow(content string) string {
	return lipgloss.NewStyle().Width(cardWidth()).Padding(0, 2).Align(lipgloss.Center).Render(content)
}
