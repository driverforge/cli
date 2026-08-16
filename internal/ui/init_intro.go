package ui

import "strings"

// InitIntro returns the overview shown at the top of `driverforge init`, so the user
// understands the whole flow (and that nothing is written without approval)
// before the first prompt.
func InitIntro() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("Set up Driverforge in this driver"))
	b.WriteString("\n\n")
	b.WriteString(bodyStyle.Render("This will:"))
	b.WriteByte('\n')
	for _, s := range []string{
		"Embed the Anvil SDK into src/vendor/",
		"Wrap OnDriverInit so events, errors and logs are captured",
		"Link this driver to an Driverforge project",
		"Optionally add build targets (debug / release)",
	} {
		b.WriteString(bodyStyle.Render("  • " + s))
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
	b.WriteString(bodyStyle.Render("You'll review every change as a diff and approve it before anything is written."))
	b.WriteByte('\n')
	return b.String()
}
