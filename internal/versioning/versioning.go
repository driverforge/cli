// Package versioning implements the two <version> schemes a driver can choose
// during `driverforge init` (recorded as .driverforge/config.json's "versioning"). The
// integer <version> is the only version Control4 knows; Director compares it
// numerically to decide whether a .c4z is an update, so every scheme's one job
// is to produce a strictly increasing value.
package versioning

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Scheme names as stored in .driverforge/config.json.
const (
	Date    = "date"    // YYYYMMDDNN: date + 2-digit daily sequence
	Integer = "integer" // plain counter, +1 per bump, width-preserved
)

// Valid reports whether scheme is one driverforge knows how to bump.
func Valid(scheme string) bool {
	return scheme == Date || scheme == Integer
}

// Next returns the next <version> for the scheme, given the authored current
// value.
//
// date: max(<today>01, current+1) — the first build of a day takes its first
// slot; later builds tick the sequence. A day with more than 99 bumps spills
// into the next day's numeric space, and a current value from the future (or
// any larger pre-existing integer) keeps ticking +1 until the date overtakes it —
// monotonic in every case, never an error.
//
// integer: current+1, preserving the authored width ("000042" -> "000043").
func Next(scheme, current string, now time.Time) (string, error) {
	current = strings.TrimSpace(current)
	switch scheme {
	case Date:
		base := (now.Year()*10000+int(now.Month())*100+now.Day())*100 + 1
		next := base
		if cur, err := strconv.Atoi(current); err == nil && cur+1 > base {
			next = cur + 1
		}
		return strconv.Itoa(next), nil
	case Integer:
		n, err := strconv.Atoi(current)
		if err != nil {
			return "", fmt.Errorf("cannot increment non-numeric <version> %q", current)
		}
		return fmt.Sprintf("%0*d", len(current), n+1), nil
	default:
		return "", fmt.Errorf("unknown versioning scheme %q", scheme)
	}
}

// Detect suggests the scheme an existing driver already uses, from its
// authored <version>: an 8- or 10-digit value whose leading eight digits parse
// as a plausible calendar date (1990-2100) reads as date-based; anything else
// is treated as a plain counter — never silently converting someone's own
// numbering (the init interview states the suggestion for confirmation).
func Detect(current string) string {
	v := strings.TrimSpace(current)
	if len(v) != 8 && len(v) != 10 {
		return Integer
	}
	d, err := time.Parse("20060102", v[:8])
	if err != nil || d.Year() < 1990 || d.Year() > 2100 {
		return Integer
	}
	return Date
}
