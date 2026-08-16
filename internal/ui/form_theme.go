package ui

import (
	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Form palette — reuses the shared brand accents (colInfo / colInfoBright) plus
// semantic green/red, so prompts match the rest of the CLI rather than huh's
// plain default.
var (
	formGreen  = lipgloss.Color("42")  // selected / affirmative
	formRed    = lipgloss.Color("196") // errors
	formMuted  = lipgloss.Color("241") // dim prefixes / placeholders
	formDesc   = lipgloss.Color("250") // field descriptions / hints — readable, secondary
	formNormal = lipgloss.Color("252") // option text
)

// promptGap selects which element of the prompt block carries the blank line
// before the choices: the description when a field has one, else the title.
type promptGap int

const (
	gapDescription promptGap = iota // blank line after the description
	gapTitle                        // blank line after the title (no description)
)

// formTheme is the brand huh theme for fields that have a description — the blank
// line before the choices sits under the description.
func formTheme() huh.Theme { return buildFormTheme(gapDescription) }

// formThemeTitleGap is for description-less selects, so the blank line before the
// choices sits under the title instead.
func formThemeTitleGap() huh.Theme { return buildFormTheme(gapTitle) }

// buildFormTheme is the brand huh theme applied to every interactive form via
// WithTheme. It tints titles and the focus bar sky-blue, marks selections green,
// gives select/multiselect a "❯" caret and text inputs a brand prompt/cursor, and
// separates the prompt block from the choices with one blank line.
func buildFormTheme(gap promptGap) huh.Theme {
	return huh.ThemeFunc(func(isDark bool) *huh.Styles {
		t := huh.ThemeBase(isDark)

		// Always separate a form from the output above it with a single blank line.
		t.Form.Base = t.Form.Base.MarginTop(1)

		// Titles + the focused field's left bar.
		t.Focused.Base = t.Focused.Base.BorderForeground(colInfo)
		t.Focused.Card = t.Focused.Base
		t.Focused.Title = t.Focused.Title.Foreground(colInfo).Bold(true)
		t.Focused.NoteTitle = t.Focused.NoteTitle.Foreground(colInfo).Bold(true)
		t.Focused.Description = t.Focused.Description.Foreground(formDesc)

		// One blank line between the prompt block and the first choice.
		switch gap {
		case gapTitle:
			t.Focused.Title = t.Focused.Title.MarginBottom(1)
		default:
			t.Focused.Description = t.Focused.Description.MarginBottom(1)
		}
		t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(formRed)
		t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(formRed)

		// Single-select.
		t.Focused.SelectSelector = lipgloss.NewStyle().Foreground(colInfoBright).SetString("❯ ")
		t.Focused.Option = t.Focused.Option.Foreground(formNormal)
		t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(colInfoBright)
		t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(colInfoBright)

		// Multi-select.
		t.Focused.MultiSelectSelector = lipgloss.NewStyle().Foreground(colInfoBright).SetString("❯ ")
		t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(formGreen)
		t.Focused.SelectedPrefix = lipgloss.NewStyle().Foreground(formGreen).SetString("✓ ")
		t.Focused.UnselectedPrefix = lipgloss.NewStyle().Foreground(formMuted).SetString("  ")
		t.Focused.UnselectedOption = t.Focused.UnselectedOption.Foreground(formNormal)

		// Text input.
		t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(colInfo)
		t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(colInfoBright)
		t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(formMuted)

		// Confirm buttons: filled brand accent for the focused choice.
		t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(colBack).Background(colInfo).Bold(true)
		t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(formNormal).Background(lipgloss.Color("237"))
		t.Focused.Next = t.Focused.FocusedButton

		// Blurred mirrors focused but hides the bar (huh convention).
		t.Blurred = t.Focused
		t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
		t.Blurred.Card = t.Blurred.Base
		t.Blurred.NextIndicator = lipgloss.NewStyle()
		t.Blurred.PrevIndicator = lipgloss.NewStyle()

		// Group/form titles align with field titles.
		t.Group.Title = t.Focused.Title
		t.Group.Description = t.Focused.Description

		return t
	})
}
