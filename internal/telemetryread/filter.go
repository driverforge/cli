package telemetryread

import (
	"bytes"
	"regexp"
	"slices"
	"time"
)

// Filter selects entries. Every set field must match (AND). The same filter is
// sent to the server where the graph supports it, and always applied here as
// well, so a live entry is held to exactly the rules a queried one is.
type Filter struct {
	Kind          Kind
	Controller    string   // hardware id (MAC); "" for any controller
	Drivers       []string // driver files; empty for any
	DriverVersion string   // "" for any version
	Levels        []string // lower-case; logs and errors
	Name          string   // event name
	CorrelationID string
	Fields        map[string]string // facet key → value, see FieldKeys
	Grep          *regexp.Regexp    // message or serialised payload
	Since, Until  time.Time         // zero for open-ended
}

// FieldKeys are the facets --field accepts for each type: the facets the web
// app filters each view by.
var FieldKeys = map[Kind][]string{
	KindLog:   {"controller", "driver", "level"},
	KindError: {"controller", "driver"},
	KindEvent: {"controller", "driver", "name", "status"},
}

// fieldValue is the entry's value for a facet key.
func fieldValue(e Entry, key string) string {
	switch key {
	case "controller":
		return e.Device.ID
	case "driver":
		return e.Driver
	case "level":
		return deref(e.Level)
	case "name":
		return deref(e.Name)
	case "status":
		return deref(e.Status)
	}
	return ""
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Match reports whether e passes every filter.
func (f Filter) Match(e Entry) bool {
	switch {
	case f.Kind != "" && e.Type != f.Kind,
		f.Controller != "" && e.Device.ID != f.Controller,
		len(f.Drivers) > 0 && !slices.Contains(f.Drivers, e.Driver),
		f.DriverVersion != "" && deref(e.DriverVersion) != f.DriverVersion,
		len(f.Levels) > 0 && !slices.Contains(f.Levels, deref(e.Level)),
		f.Name != "" && deref(e.Name) != f.Name,
		f.CorrelationID != "" && deref(e.CorrelationID) != f.CorrelationID,
		!f.Since.IsZero() && e.at.Before(f.Since),
		!f.Until.IsZero() && e.at.After(f.Until):
		return false
	}
	for k, v := range f.Fields {
		if fieldValue(e, k) != v {
			return false
		}
	}
	return f.Grep == nil || Matches(f.Grep, e)
}

// Matches reports whether re matches the entry's message or its serialised
// payload — the text --grep and --until-match search.
func Matches(re *regexp.Regexp, e Entry) bool {
	if e.Message != nil && re.MatchString(*e.Message) {
		return true
	}
	p := bytes.TrimSpace(e.Payload)
	return len(p) > 0 && !bytes.Equal(p, []byte("null")) && re.Match(p)
}

// ClientOnly reports whether the filter needs entries the server can't select
// for it, so a page of N server results may hold fewer than N matches.
func (f Filter) ClientOnly() bool {
	if f.Grep != nil || !f.Until.IsZero() || !f.Since.IsZero() {
		return true
	}
	switch f.Kind {
	case KindError:
		// Errors have no server-side level or name filter.
		return len(f.Levels) > 0 || f.Name != "" || f.Fields["name"] != ""
	case KindEvent:
		// Events have no server-side correlation filter.
		return f.CorrelationID != ""
	case KindLog:
		return f.Name != ""
	}
	return false
}
