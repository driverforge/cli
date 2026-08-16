package ui

import (
	"testing"

	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

func TestPropertiesFormSkipsNonInteractiveProps(t *testing.T) {
	// Only interactive types are prompted; everything else is skipped entirely
	// (not prompted, not returned). With no interactive fields the form must not
	// call form.Run (which needs a terminal), so this runs headless.
	props := []driverxml.Property{
		{Name: "Section", Type: driverxml.TypeLabel, Default: "Network settings"},
		{Name: "Firmware", Type: driverxml.TypeString, Default: "1.0", ReadOnly: true},
		{Name: "Zone", Type: driverxml.TypeDynamicList},
		{Name: "Docs", Type: driverxml.TypeLink, Default: "https://example.com"},
		{Name: "Bind", Type: driverxml.TypeDeviceSelector},
		{Name: "Accent", Type: driverxml.TypeColorSelector},
		{Name: "Mystery", Type: "SOME_FUTURE_TYPE"}, // unrecognized → skipped, not free-texted
	}
	got, err := PropertiesForm("Test", props)
	if err != nil {
		t.Fatalf("form with only non-interactive props should not error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("non-interactive and unknown property types should be skipped, got %v", got)
	}
}

func TestPropertiesFormSkipsUnnamed(t *testing.T) {
	got, err := PropertiesForm("Test", []driverxml.Property{{Name: "", Type: driverxml.TypeString}})
	if err != nil {
		t.Fatalf("PropertiesForm: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("unnamed property should be skipped: %v", got)
	}
}

func TestListOptionsIncludesDefaultNotInItems(t *testing.T) {
	p := driverxml.Property{Type: driverxml.TypeList, Default: "Legacy", Items: []string{"Auto", "Manual"}}
	opts := listOptions(p)
	if len(opts) != 3 {
		t.Fatalf("want 3 options (items + stale default), got %d", len(opts))
	}
	if opts[2].Key != "Legacy" {
		t.Errorf("stale default should be appended/selectable, got options %+v", opts)
	}
}

func TestListOptionsDedupes(t *testing.T) {
	p := driverxml.Property{Type: driverxml.TypeList, Default: "Auto", Items: []string{"Auto", "Manual", "Auto"}}
	if got := len(listOptions(p)); got != 2 {
		t.Errorf("duplicate items/default should dedupe to 2, got %d", got)
	}
}

func TestIntValidator(t *testing.T) {
	min, max := 1, 10
	v := intValidator(driverxml.Property{Min: &min, Max: &max})
	cases := []struct {
		in    string
		valid bool
	}{
		{"", true},     // empty allowed
		{"5", true},    // in range
		{"1", true},    // lower bound
		{"10", true},   // upper bound
		{"0", false},   // below min
		{"11", false},  // above max
		{"abc", false}, // not a number
		{" 7 ", true},  // trimmed
	}
	for _, c := range cases {
		err := v(c.in)
		if c.valid && err != nil {
			t.Errorf("%q should be valid, got %v", c.in, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%q should be invalid", c.in)
		}
	}
}

func TestRangeHint(t *testing.T) {
	min, max := 1, 65535
	if got := rangeHint(driverxml.Property{Min: &min, Max: &max}); got != "1–65535" {
		t.Errorf("range hint = %q", got)
	}
	if got := rangeHint(driverxml.Property{Min: &min}); got != "≥ 1" {
		t.Errorf("min-only hint = %q", got)
	}
	if got := rangeHint(driverxml.Property{}); got != "" {
		t.Errorf("no-bounds hint should be empty, got %q", got)
	}
}

func TestBoolStringAndTruthy(t *testing.T) {
	if boolString(true) != "True" || boolString(false) != "False" {
		t.Error("boolString should use Control4 True/False spelling")
	}
	for _, s := range []string{"true", "True", "1", "yes", "ON"} {
		if !truthy(s) {
			t.Errorf("%q should be truthy", s)
		}
	}
	for _, s := range []string{"false", "0", "no", ""} {
		if truthy(s) {
			t.Errorf("%q should be falsey", s)
		}
	}
}
