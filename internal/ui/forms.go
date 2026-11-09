package ui

import (
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
)

// Choice is a label/value pair for PickOne.
type Choice struct {
	Label string
	Value string
}

// PickOne presents an interactive single-select list and returns the chosen
// value. def preselects the matching option. It is generic so callers (e.g. org
// selection) don't couple the UI layer to their domain types.
func PickOne(title string, choices []Choice, def string) (string, error) {
	return PickOneDesc(title, "", choices, def)
}

// PickOneDesc is PickOne with a description line shown under the title — use it
// when the choice benefits from a sentence of context.
func PickOneDesc(title, description string, choices []Choice, def string) (string, error) {
	if err := checkPrompt(title); err != nil {
		return "", err
	}
	selected := def
	opts := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		opts[i] = huh.NewOption(c.Label, c.Value)
	}
	// Put the blank line before the options under the description when there is
	// one, else under the title.
	theme := formTheme()
	if description == "" {
		theme = formThemeTitleGap()
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().Title(title).Description(description).Options(opts...).Value(&selected),
		),
	).WithTheme(theme).WithWidth(formWidth())
	if err := form.Run(); err != nil {
		return "", ExpectedIfAborted(err)
	}
	return selected, nil
}

// PromptText presents a single text input prompt, pre-filled with def, and
// returns the (trimmed) entered value. An empty answer falls back to def — use
// it for a "suggested value you probably want" (e.g. a default name/branch).
func PromptText(title, def string) (string, error) {
	return PromptTextDesc(title, "", def)
}

// PromptTextDesc presents a described text input, pre-filled with def. An
// empty answer falls back to def.
func PromptTextDesc(title, description, def string) (string, error) {
	v, err := promptInput(title, description, def)
	if err != nil {
		return "", err
	}
	if v == "" {
		return def, nil
	}
	return v, nil
}

// PromptOptional presents a described text input pre-filled with suggested, and
// returns exactly what the user leaves (empty stays empty) — use it for optional
// fields where clearing the suggestion means "none", not "fall back to it".
func PromptOptional(title, description, suggested string) (string, error) {
	return promptInput(title, description, suggested)
}

// promptInput renders the input and returns the trimmed value. The blank line
// before the input sits under the description when there is one, else the title.
func promptInput(title, description, initial string) (string, error) {
	if err := checkPrompt(title); err != nil {
		return "", err
	}
	v := initial
	theme := formTheme()
	if description == "" {
		theme = formThemeTitleGap()
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().Title(title).Description(description).Value(&v),
		),
	).WithTheme(theme).WithWidth(formWidth())
	if err := form.Run(); err != nil {
		return "", ExpectedIfAborted(err)
	}
	return strings.TrimSpace(v), nil
}

// MultiSelect presents an interactive checklist (all options pre-selected) and
// returns the chosen values. Used by `driverforge upgrade` to opt into each available
// upgrade.
func MultiSelect(title string, choices []Choice) ([]string, error) {
	if err := checkPrompt(title); err != nil {
		return nil, err
	}
	selected := make([]string, 0, len(choices))
	opts := make([]huh.Option[string], len(choices))
	for i, c := range choices {
		opts[i] = huh.NewOption(c.Label, c.Value).Selected(true)
		selected = append(selected, c.Value)
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().Title(title).Options(opts...).Value(&selected),
		),
	).WithTheme(formThemeTitleGap()).WithWidth(formWidth())
	if err := form.Run(); err != nil {
		return nil, ExpectedIfAborted(err)
	}
	return selected, nil
}

// Confirm presents an interactive yes/no prompt, returning the choice. def is
// the pre-selected answer.
func Confirm(title, description string, def bool) (bool, error) {
	if err := checkPrompt(title); err != nil {
		return false, err
	}
	v := def
	form := huh.NewForm(
		huh.NewGroup(
			// Left-align the buttons so Yes/No anchor to the left margin instead of
			// centering under the title/description — otherwise huh re-centers them
			// per prompt and they appear to jump horizontally with the message width.
			huh.NewConfirm().Title(title).Description(description).Value(&v).
				WithButtonAlignment(lipgloss.Left),
		),
	).WithTheme(formTheme()).WithWidth(formWidth())
	if err := form.Run(); err != nil {
		return false, ExpectedIfAborted(err)
	}
	return v, nil
}
