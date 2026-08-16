package ui

import (
	"fmt"

	"github.com/driverforge/cli/pkg/c4z"
)

// StepLabel maps a pipeline step to a human-readable label.
func StepLabel(s c4z.Step) string {
	switch s {
	case c4z.StepResolve:
		return "Resolve project"
	case c4z.StepVersion:
		return "Version"
	case c4z.StepManifest:
		return "Parse manifest"
	case c4z.StepPrepackage:
		return "Prepackage commands"
	case c4z.StepPrepare:
		return "Prepare sources"
	case c4z.StepSquish:
		return "Squish Lua"
	case c4z.StepDriverXML:
		return "Transform driver.xml"
	case c4z.StepEncrypt:
		return "Encrypt"
	case c4z.StepPack:
		return "Package .c4z"
	case c4z.StepUnpack:
		return "Unpack"
	case c4z.StepSourcemap:
		return "Source map"
	case c4z.StepDone:
		return "Done"
	default:
		return string(s)
	}
}

// versionLabel formats the version for display: the driver's integer
// <version>, or a dash when the driver doesn't declare one.
func versionLabel(r c4z.Result) string {
	if r.Version == "" {
		return "—"
	}
	return r.Version
}

// SuccessBlock renders a completed operation as a green left-bar block: the
// headline, an optional subdued suffix (e.g. a version), then aligned key/value
// context rows. Shared by the deploy and sync summaries so they read alike.
func SuccessBlock(headline, suffix string, rows [][2]string) string {
	head := okStyle.Render("✓ " + headline)
	if suffix != "" {
		head += "  " + mutedStyle.Render(suffix)
	}
	lines := []string{head}
	if len(rows) > 0 {
		width := 0
		for _, kv := range rows {
			if len(kv[0]) > width {
				width = len(kv[0])
			}
		}
		lines = append(lines, "")
		for _, kv := range rows {
			lines = append(lines, keyStyle.Render(fmt.Sprintf("%-*s ", width, kv[0]))+valStyle.Render(kv[1]))
		}
	}
	return calloutBlock(colOk, lines)
}

// WarningBlock renders an advisory as an amber left-bar block: a headline,
// aligned key/value context rows, and — grouped beneath, in the brighter gold
// the error cards use — a 💡 tip. The guidance-card shape, for conditions
// that inform rather than fail (e.g. `driverforge init` on an already-set-up
// project).
func WarningBlock(headline string, rows [][2]string, tip string) string {
	lines := []string{blockRow(colInk, true, headline)}
	if len(rows) > 0 {
		width := 0
		for _, kv := range rows {
			if len(kv[0]) > width {
				width = len(kv[0])
			}
		}
		lines = append(lines, "")
		for _, kv := range rows {
			lines = append(lines, keyStyle.Render(fmt.Sprintf("%-*s ", width, kv[0]))+valStyle.Render(kv[1]))
		}
	}
	if tip != "" {
		lines = append(lines, "", blockRow(colTip, true, " 💡  "+linkify(tip)))
	}
	return calloutBlock(colAmber, lines)
}

// InfoBlock renders an informational callout: a brand-blue left-bar block with
// a headline and follow-up lines (URLs linkified so terminals that support
// OSC-8 make them clickable).
func InfoBlock(headline string, lines ...string) string {
	rows := []string{bodyStyle.Bold(true).Render(headline)}
	if len(lines) > 0 {
		rows = append(rows, "")
		for _, l := range lines {
			rows = append(rows, bodyStyle.Render(linkify(l)))
		}
	}
	return calloutBlock(colInfo, rows)
}
