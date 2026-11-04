package telemetryread

import (
	"context"
	"regexp"
	"strconv"
	"sync"
	"testing"

	"github.com/driverforge/cli/internal/driverforgeapi"
)

// fakeSource serves entries (any order) newest first, honouring cursor and
// limit like the graph: the cursor is epoch ms and pages strictly before it.
type fakeSource struct {
	mu      sync.Mutex
	entries []Entry
	calls   int
	err     error
}

func (s *fakeSource) add(e ...Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e...)
}

func (s *fakeSource) Page(_ context.Context, cursor string, limit int) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return Page{}, s.err
	}
	newest := make([]Entry, len(s.entries))
	copy(newest, s.entries)
	Oldest(newest)
	for i, j := 0, len(newest)-1; i < j; i, j = i+1, j-1 {
		newest[i], newest[j] = newest[j], newest[i]
	}
	var before int64 = 1 << 62
	if cursor != "" {
		before, _ = strconv.ParseInt(cursor, 10, 64)
	}
	var page Page
	for _, e := range newest {
		if e.at.UnixMilli() >= before {
			continue
		}
		if len(page.Entries) == limit {
			page.HasMore = true
			break
		}
		page.Entries = append(page.Entries, e)
	}
	if page.HasMore {
		page.Cursor = Cursor(page.Entries[len(page.Entries)-1])
	}
	page.WindowMinutes = 1440
	return page, nil
}

func logAt(id string, ms int64, msg string) Entry {
	return FromLog(driverforgeapi.LogEntry{ID: id, CreatedAt: float64(ms), Message: msg, Level: "INFO"}, nil)
}

func ids(es []Entry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.ID
	}
	return out
}

func TestFetchTailIsNewestOldestFirst(t *testing.T) {
	src := &fakeSource{}
	for i := 1; i <= 8; i++ {
		src.add(logAt("l"+strconv.Itoa(i), int64(i*1000), "m"))
	}
	res, err := Fetch(context.Background(), src, Query{Tail: 5})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Entries); len(got) != 5 || got[0] != "l4" || got[4] != "l8" {
		t.Fatalf("entries = %v, want l4..l8 oldest first", got)
	}
	if !res.HasMore || res.NextCursor != "4000" {
		t.Errorf("has_more %v cursor %q, want true and 4000", res.HasMore, res.NextCursor)
	}

	older, _ := Fetch(context.Background(), src, Query{Tail: 5, Before: res.NextCursor})
	if got := ids(older.Entries); len(got) != 3 || got[0] != "l1" || older.HasMore || older.NextCursor != "" {
		t.Errorf("older page = %v has_more %v cursor %q", got, older.HasMore, older.NextCursor)
	}
}

func TestFetchTiesBreakByID(t *testing.T) {
	src := &fakeSource{}
	src.add(logAt("b", 1000, "m"), logAt("a", 1000, "m"))
	res, _ := Fetch(context.Background(), src, Query{Tail: 10})
	if got := ids(res.Entries); got[0] != "a" || got[1] != "b" {
		t.Errorf("entries = %v, want a before b", got)
	}
}

func TestFetchPagesThroughClientSideFilter(t *testing.T) {
	src := &fakeSource{}
	for i := 1; i <= 2500; i++ {
		msg := "noise"
		if i%1000 == 0 {
			msg = "vendor response"
		}
		src.add(logAt("l"+strconv.Itoa(i), int64(i), msg))
	}
	res, err := Fetch(context.Background(), src, Query{Tail: 5, Filter: Filter{Grep: regexp.MustCompile("vendor")}})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(res.Entries); len(got) != 2 || got[0] != "l1000" || got[1] != "l2000" {
		t.Errorf("entries = %v, want l1000 and l2000", got)
	}
	if res.HasMore || src.calls != 3 {
		t.Errorf("has_more %v after %d pages, want false after 3", res.HasMore, src.calls)
	}
}
