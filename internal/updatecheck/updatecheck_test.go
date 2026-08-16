package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"1.2.3", "1.2.3", 0, true},
		{"1.2.4", "1.2.3", 1, true},
		{"1.2.3", "1.2.4", -1, true},
		{"1.10.0", "1.9.9", 1, true}, // numeric, not lexical
		{"2.0.0", "1.99.99", 1, true},
		{"v1.2.3", "1.2.3", 0, true},     // leading v ignored
		{"1.2.3-next", "1.2.3", 0, true}, // pre-release suffix ignored
		{"dev", "1.2.3", 0, false},       // non-release -> ok=false
		{"1.2", "1.2.0", 0, false},       // not 3-part -> ok=false
		// The agent's date-stamped integer scheme (YYYYMMDDNN).
		{"2026072201", "2026072104", 1, true},
		{"2026072104", "2026072201", -1, true},
		{"2026072104", "2026072104", 0, true},
		{"2026072201", "0.6.0", 0, false}, // mixed schemes -> ok=false
		{"0.6.0", "2026072104", 0, false},
		{"2026072201", "unknown", 0, false},
	}
	for _, c := range cases {
		got, ok := compareVersions(c.a, c.b)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("compareVersions(%q,%q) = (%d,%v), want (%d,%v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestLatestFromManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"version":"1.4.0","tag":"v1.4.0","artifacts":[{"name":"driverforge_1.4.0_darwin_arm64.tar.gz"}]}`))
		case "/noversion":
			w.Write([]byte(`{"tag":"v1.4.0"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := latestFromManifest(context.Background(), srv.URL+"/ok")
	if err != nil || got != "1.4.0" {
		t.Fatalf("latestFromManifest(ok) = (%q,%v), want (1.4.0,nil)", got, err)
	}
	if _, err := latestFromManifest(context.Background(), srv.URL+"/noversion"); err == nil {
		t.Error("expected error when manifest has no version")
	}
	if _, err := latestFromManifest(context.Background(), srv.URL+"/missing"); err == nil {
		t.Error("expected error on HTTP 404")
	}
}

// isolateCache points UserCacheDir at a temp dir so the tests don't touch the
// real update-check cache, and returns a helper to read it back.
func isolateCache(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	// os.UserCacheDir honours these per-platform.
	t.Setenv("XDG_CACHE_HOME", dir) // Linux
	t.Setenv("HOME", dir)           // macOS ($HOME/Library/Caches)
	t.Setenv("LocalAppData", dir)   // Windows
}

// A failed fetch must record the attempt (CheckedAt) so the TTL is honoured —
// otherwise a blackholed network re-hits the manifest on every command.
func TestFailedFetchBackdatesCache(t *testing.T) {
	isolateCache(t)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	// First check fails and writes a backdated cache entry.
	if got := cachedOrFetchLatest(context.Background(), srv.URL); got != "" {
		t.Errorf("failed fetch = %q, want empty", got)
	}
	c, ok := readCache()
	if !ok || c.CheckedAt.IsZero() {
		t.Fatal("a failed fetch must still record CheckedAt")
	}
	// Second check within the TTL must NOT hit the network again.
	cachedOrFetchLatest(context.Background(), srv.URL)
	if hits != 1 {
		t.Errorf("expected 1 network hit (TTL honoured after failure), got %d", hits)
	}
}

// A failed fetch keeps a previously cached good version so notifications still work.
func TestFailedFetchKeepsStaleVersion(t *testing.T) {
	isolateCache(t)
	writeCache(cacheData{LatestVersion: "2.0.0", CheckedAt: time.Now().Add(-48 * time.Hour)}) // stale

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if got := cachedOrFetchLatest(context.Background(), srv.URL); got != "2.0.0" {
		t.Errorf("stale-fallback = %q, want 2.0.0", got)
	}
	if c, _ := readCache(); c.LatestVersion != "2.0.0" || time.Since(c.CheckedAt) > time.Minute {
		t.Errorf("failed fetch should keep the version but refresh CheckedAt: %+v", c)
	}
}

// LatestCLI is the explicit `driverforge upgrade` path: it must bypass even a
// fresh cache (a day-old answer would hide a release the user knows about)
// and refresh the cache with what it fetched.
func TestLatestCLIBypassesCache(t *testing.T) {
	isolateCache(t)
	writeCache(cacheData{LatestVersion: "1.5.0", CheckedAt: time.Now()}) // fresh but stale content
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"version":"1.6.0"}`))
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_UPDATE_MANIFEST_URL", srv.URL)

	if got := LatestCLI(context.Background()); got != "1.6.0" {
		t.Errorf("LatestCLI = %q, want the live 1.6.0, not the cached 1.5.0", got)
	}
	if c, _ := readCache(); c.LatestVersion != "1.6.0" {
		t.Errorf("cache after LatestCLI = %q, want refreshed to 1.6.0", c.LatestVersion)
	}
}

// A failed live fetch reports unknown ("") and leaves the cache untouched for
// the passive notifier.
func TestLatestCLIFailureLeavesCache(t *testing.T) {
	isolateCache(t)
	writeCache(cacheData{LatestVersion: "1.5.0", CheckedAt: time.Now()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_UPDATE_MANIFEST_URL", srv.URL)

	if got := LatestCLI(context.Background()); got != "" {
		t.Errorf("LatestCLI on failure = %q, want empty (couldn't check)", got)
	}
	if c, _ := readCache(); c.LatestVersion != "1.5.0" {
		t.Errorf("a failed explicit check must not clobber the cache: %+v", c)
	}
}

// A fresh cache is served without any network call.
func TestFreshCacheSkipsNetwork(t *testing.T) {
	isolateCache(t)
	writeCache(cacheData{LatestVersion: "1.5.0", CheckedAt: time.Now()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("fresh cache must not hit the network")
	}))
	defer srv.Close()
	if got := cachedOrFetchLatest(context.Background(), srv.URL); got != "1.5.0" {
		t.Errorf("fresh cache = %q, want 1.5.0", got)
	}
}
