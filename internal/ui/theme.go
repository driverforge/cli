package ui

import "charm.land/lipgloss/v2"

// Semantic accent palette. Each colour carries one meaning so panels read
// consistently: violet/indigo for brand + informational surfaces (titles,
// spinners, the login card), amber for warnings, red for errors. Amber and red
// live with the error renderer (see error.go); the brand accent is here because
// the shared TUI styles below build on it.
var (
	colInfo       = lipgloss.Color("#79C0FF") // brand sky-blue: borders, titles, spinners
	colInfoBright = lipgloss.Color("#A8D8FF") // brighter blue for emphasised content
	colOk         = lipgloss.Color("42")      // success green: ✓ and the build-complete accent
)

// Styles for the TUI. lipgloss auto-detects the terminal colour profile and
// honours NO_COLOR / non-TTY output, degrading gracefully.
var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(colInfo)
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	okStyle      = lipgloss.NewStyle().Foreground(colOk)
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	warnStyle    = lipgloss.NewStyle().Foreground(colAmber) // colAmber defined in error.go
	spinnerStyle = lipgloss.NewStyle().Foreground(colInfo)
	keyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	valStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	// bodyStyle is readable primary prose (near-white), for explanatory copy that
	// mutedStyle (a dim 241 grey) renders with too little contrast.
	bodyStyle = lipgloss.NewStyle().Foreground(colInk) // colInk defined in error.go
)
