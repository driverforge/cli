package telemetryread

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/driverforge/cli/internal/driverforgeapi"
)

// MaxPageSize is the most entries the graph returns in one page.
const MaxPageSize = 1000

// maxScanPages bounds how many pages one query reads while a client-side
// filter (--grep, a time bound, …) discards entries, so a rare match can't
// walk the whole retention window. A capped read reports has_more and a cursor
// to continue from.
const maxScanPages = 10

// Page is one page of entries, newest first, with the graph's paging state.
type Page struct {
	Entries []Entry
	driverforgeapi.TelemetryPage
}

// Source fetches one page of entries, newest first, strictly older than the
// cursor ("" for the newest).
type Source interface {
	Page(ctx context.Context, cursor string, limit int) (Page, error)
}

// Query is a read of the newest entries matching a filter.
type Query struct {
	Filter Filter
	Tail   int    // how many matching entries to return
	Before string // cursor from a previous Result.NextCursor; "" for the newest
}

// Result is a query's entries, oldest first.
type Result struct {
	Entries       []Entry
	HasMore       bool   // older matching entries may exist
	NextCursor    string // pass as Query.Before to read them
	WindowMinutes int    // look-back the server applied
}

// Fetch reads the newest q.Tail entries matching q.Filter, paging back through
// src as far as the client-side filters need (up to maxScanPages), and returns
// them oldest first.
func Fetch(ctx context.Context, src Source, q Query) (Result, error) {
	cursor := q.Before
	if cursor == "" && !q.Filter.Until.IsZero() {
		// Start just after --until rather than paging down from now.
		cursor = strconv.FormatInt(q.Filter.Until.UnixMilli()+1, 10)
	}
	size := q.Tail
	if q.Filter.ClientOnly() || size > MaxPageSize {
		size = MaxPageSize
	}

	var res Result
	var got []Entry // newest first
	for range maxScanPages {
		page, err := src.Page(ctx, cursor, size)
		if err != nil {
			return Result{}, err
		}
		res.WindowMinutes = page.WindowMinutes
		for i, e := range page.Entries {
			if !q.Filter.Match(e) {
				continue
			}
			got = append(got, e)
			if len(got) == q.Tail {
				res.HasMore = i < len(page.Entries)-1 || page.HasMore
				if res.HasMore {
					res.NextCursor = Cursor(e)
				}
				res.Entries = Oldest(got)
				return res, nil
			}
		}
		if !page.HasMore || page.Cursor == "" {
			res.Entries = Oldest(got)
			return res, nil
		}
		cursor = page.Cursor
	}
	res.HasMore, res.NextCursor = true, cursor
	res.Entries = Oldest(got)
	return res, nil
}

// Cursor is the paging cursor that continues strictly before e: its own time in
// epoch milliseconds, the graph's cursor form.
func Cursor(e Entry) string { return strconv.FormatInt(e.at.UnixMilli(), 10) }

// Oldest sorts entries oldest first, ties broken by id, in place.
func Oldest(entries []Entry) []Entry {
	slices.SortStableFunc(entries, func(a, b Entry) int {
		if c := a.at.Compare(b.at); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return entries
}

// GraphSource reads one type of entry from the graph for a set of projects.
type GraphSource struct {
	API              *driverforgeapi.Client
	Filter           Filter
	ProjectIDs       []string
	TimeRangeMinutes int
	Names            Names
}

// Page implements Source, pushing every filter the graph supports to the
// server. Fetch re-applies the full filter to what comes back.
func (s GraphSource) Page(ctx context.Context, cursor string, limit int) (Page, error) {
	q := driverforgeapi.TelemetryQuery{
		ProjectIDs:       s.ProjectIDs,
		TimeRangeMinutes: s.TimeRangeMinutes,
		Limit:            limit,
		Cursor:           cursor,
	}
	f := s.Filter
	controllers := one(first(f.Controller, f.Fields["controller"]))
	drivers := f.Drivers
	if d := f.Fields["driver"]; d != "" && len(drivers) == 0 {
		drivers = []string{d}
	}
	versions := one(f.DriverVersion)

	var page Page
	switch f.Kind {
	case KindLog:
		levels := f.Levels
		if l := f.Fields["level"]; l != "" && len(levels) == 0 {
			levels = []string{l}
		}
		upper := make([]string, len(levels))
		for i, l := range levels {
			upper[i] = strings.ToUpper(l)
		}
		logs, p, err := s.API.Logs(ctx, q, driverforgeapi.LogFilters{
			Levels: upper, Controllers: controllers, Drivers: drivers,
			DriverVersions: versions, TraceID: f.CorrelationID,
		})
		if err != nil {
			return Page{}, err
		}
		page.TelemetryPage = p
		for _, l := range logs {
			page.Entries = append(page.Entries, FromLog(l, s.Names))
		}
	case KindEvent:
		events, p, err := s.API.Events(ctx, q, driverforgeapi.EventFilters{
			EventNames: one(first(f.Name, f.Fields["name"])), Statuses: one(f.Fields["status"]),
			Controllers: controllers, Drivers: drivers, DriverVersions: versions,
		})
		if err != nil {
			return Page{}, err
		}
		page.TelemetryPage = p
		for _, e := range events {
			page.Entries = append(page.Entries, FromEvent(e, s.Names))
		}
	case KindError:
		errs, p, err := s.API.ErrorOccurrences(ctx, q, driverforgeapi.ErrorFilters{
			Controllers: controllers, Drivers: drivers, DriverVersions: versions,
			TraceID: f.CorrelationID,
		})
		if err != nil {
			return Page{}, err
		}
		page.TelemetryPage = p
		for _, e := range errs {
			page.Entries = append(page.Entries, FromError(e, s.Names))
		}
	}
	return page, nil
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func one(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
