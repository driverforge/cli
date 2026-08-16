package versioning

import (
	"testing"
	"time"
)

var buildDay = time.Date(2026, 7, 6, 15, 0, 0, 0, time.UTC) // slots 2026070601+

func TestNextDate(t *testing.T) {
	cases := []struct {
		name    string
		current string
		want    string
	}{
		{"first build of the day", "2026070503", "2026070601"},
		{"second build same day", "2026070601", "2026070602"},
		{"counter legacy jumps to date space", "1022", "2026070601"},
		{"stripped legacy jumps to date space", "1100", "2026070601"},
		{"no or junk current takes today's slot", "", "2026070601"},
		{"non-numeric current takes today's slot", "v1.0", "2026070601"},
		{"day overflow spills, stays monotonic", "2026070699", "2026070700"},
		{"future-dated current keeps ticking", "2026123101", "2026123102"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Next(Date, tc.current, buildDay)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("Next(date, %q) = %q, want %q", tc.current, got, tc.want)
			}
		})
	}
}

func TestNextInteger(t *testing.T) {
	cases := []struct {
		current, want string
		wantErr       bool
	}{
		{"1022", "1023", false},
		{"000042", "000043", false},
		{"9", "10", false},
		{"", "", true},
		{"v1", "", true},
	}
	for _, tc := range cases {
		got, err := Next(Integer, tc.current, buildDay)
		if (err != nil) != tc.wantErr {
			t.Fatalf("Next(integer, %q) err = %v, wantErr %v", tc.current, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("Next(integer, %q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

func TestNextUnknownScheme(t *testing.T) {
	if _, err := Next("nonsense", "1", buildDay); err == nil {
		t.Fatal("unknown scheme must error")
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		current, want string
	}{
		{"20260705", Date},
		{"2026070501", Date},
		{"20991231", Date},
		{"1022", Integer},        // plain counter
		{"000377", Integer},      // padded counter
		{"10221201", Integer},    // 8 digits but year 1022 — not a plausible date
		{"20261399", Integer},    // month 13 — not a date
		{"123456789", Integer},   // 9 digits — no scheme match
		{"", Integer},            // absent
		{"banana", Integer},      // non-numeric
		{"99999999999", Integer}, // too long
	}
	for _, tc := range cases {
		if got := Detect(tc.current); got != tc.want {
			t.Errorf("Detect(%q) = %q, want %q", tc.current, got, tc.want)
		}
	}
}

func TestValid(t *testing.T) {
	for scheme, want := range map[string]bool{Date: true, Integer: true, "": false, "nonsense": false} {
		if got := Valid(scheme); got != want {
			t.Errorf("Valid(%q) = %v, want %v", scheme, got, want)
		}
	}
}
