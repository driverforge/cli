package ui

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"

	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

// PropertiesForm renders an interactive form for a driver's configurable
// properties (parsed from driver.xml) and returns the collected values keyed by
// property name. driver.xml DEFAULTS are pre-filled so an installer can accept
// the driver's recommended configuration by pressing through.
//
// It prompts only for the interactive Control4 property types — those with a
// value an installer sets at deploy: STRING→text, PASSWORD (or any
// password-flagged property)→masked, LIST→select, INTEGER/RANGED_INTEGER/SCROLL/
// TRACK→validated integer, RANGED_FLOAT→validated number, BOOL→yes/no. Every
// other property is skipped entirely (not shown, prompted, or returned):
// read-only, LABEL/LINK (display-only), DYNAMIC_LIST (runtime-populated),
// DEVICE_SELECTOR/COLOR_SELECTOR (no terminal picker yet), and any unrecognized
// type. Director applies the driver.xml default for anything the install
// doesn't override.
//
// Values are returned as strings using Control4's conventions ("True"/"False"
// for booleans); a driver with no editable properties returns an empty map
// without prompting.
func PropertiesForm(driverName string, props []driverxml.Property) (map[string]string, error) {
	result := make(map[string]string, len(props))
	strVals := make(map[string]*string)
	boolVals := make(map[string]*bool)
	var fields []huh.Field

	for _, p := range props {
		if p.Name == "" {
			continue
		}
		// Read-only properties are display-only regardless of type.
		if p.ReadOnly {
			continue
		}

		switch p.Type {
		case driverxml.TypeBool:
			b := truthy(p.Default)
			boolVals[p.Name] = &b
			fields = append(fields, huh.NewConfirm().Title(p.Name).Value(&b).WithButtonAlignment(lipgloss.Left))

		case driverxml.TypeList:
			v := p.Default
			strVals[p.Name] = &v
			opts := listOptions(p)
			fields = append(fields, huh.NewSelect[string]().Title(p.Name).Options(opts...).Value(&v))

		case driverxml.TypeInteger, driverxml.TypeRangedInteger, driverxml.TypeScroll, driverxml.TypeTrack:
			// Scroll/Track render as sliders in Composer, but the stored value is
			// a plain integer — collect it as one.
			v := p.Default
			strVals[p.Name] = &v
			fields = append(fields,
				huh.NewInput().Title(p.Name).Description(rangeHint(p)).Value(&v).Validate(intValidator(p)))

		case driverxml.TypeRangedFloat:
			v := p.Default
			strVals[p.Name] = &v
			fields = append(fields, huh.NewInput().Title(p.Name).Value(&v).Validate(floatValidator()))

		case driverxml.TypeString, driverxml.TypePassword:
			v := p.Default
			strVals[p.Name] = &v
			in := huh.NewInput().Title(p.Name).Value(&v)
			if p.Password {
				in = in.EchoMode(huh.EchoModePassword)
			}
			fields = append(fields, in)

		default:
			// Everything else has nothing for the installer to set here: LABEL /
			// LINK (display-only), DYNAMIC_LIST (populated by the driver at
			// runtime), DEVICE_SELECTOR / COLOR_SELECTOR (need a picker the
			// terminal can't offer yet), and any unrecognized
			// or future type. Skip it; Director applies the driver.xml default,
			// and specialized values are set later in Composer.
			continue
		}
	}

	if len(fields) > 0 {
		title := "Driver properties"
		if driverName != "" {
			title = driverName + " — properties"
		}
		if err := checkPrompt(title); err != nil {
			return nil, err
		}
		form := huh.NewForm(huh.NewGroup(fields...).Title(title)).WithTheme(formTheme()).WithWidth(formWidth())
		if err := form.Run(); err != nil {
			return nil, ExpectedIfAborted(err)
		}
	}

	for n, v := range strVals {
		result[n] = *v
	}
	for n, v := range boolVals {
		result[n] = boolString(*v)
	}
	return result, nil
}

// listOptions builds the select options for a LIST property. The declared
// default is always selectable even if it is missing from <items> (so a stale
// default never becomes unselectable).
func listOptions(p driverxml.Property) []huh.Option[string] {
	seen := make(map[string]bool, len(p.Items)+1)
	opts := make([]huh.Option[string], 0, len(p.Items)+1)
	add := func(v string) {
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		opts = append(opts, huh.NewOption(v, v))
	}
	for _, it := range p.Items {
		add(it)
	}
	add(p.Default)
	return opts
}

// intValidator validates numeric input, enforcing the RANGED_INTEGER bounds when
// present. Empty input is allowed (the property keeps no value).
func intValidator(p driverxml.Property) func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		n, err := strconv.Atoi(s)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		if p.Min != nil && n < *p.Min {
			return fmt.Errorf("must be at least %d", *p.Min)
		}
		if p.Max != nil && n > *p.Max {
			return fmt.Errorf("must be at most %d", *p.Max)
		}
		return nil
	}
}

// floatValidator accepts empty input (the property keeps no value) or any valid
// floating-point number. RANGED_FLOAT bounds aren't enforced — driverxml keeps
// integer Min/Max, which can't represent a fractional range.
func floatValidator() func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return fmt.Errorf("must be a number")
		}
		return nil
	}
}

func rangeHint(p driverxml.Property) string {
	switch {
	case p.Min != nil && p.Max != nil:
		return fmt.Sprintf("%d–%d", *p.Min, *p.Max)
	case p.Min != nil:
		return fmt.Sprintf("≥ %d", *p.Min)
	case p.Max != nil:
		return fmt.Sprintf("≤ %d", *p.Max)
	default:
		return ""
	}
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1", "yes", "on":
		return true
	default:
		return false
	}
}

// boolString renders a boolean the way Control4 driver.xml spells it.
func boolString(b bool) string {
	if b {
		return "True"
	}
	return "False"
}
