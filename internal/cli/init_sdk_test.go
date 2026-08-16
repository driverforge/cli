package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/releaseverify"
)

// isolateSDKCache points the per-user config dir (where the SDK cache lives)
// at a temp dir and seeds it with cached bundle versions.
func isolateSDKCache(t *testing.T, versions ...string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)                                      // macOS (~/Library/Application Support)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, ".config")) // Linux
	t.Setenv("AppData", filepath.Join(dir, "AppData"))         // Windows
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		p := filepath.Join(cfg, "driverforge", "sdk", v)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "driverforge-sdk.lua"), []byte("-- sdk "+v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return cfg
}

// resolveSDK is latest-first: the store's current release wins over a newer-
// looking cache state, and a cached copy of the latest needs no download (and
// no prompt).
func TestResolveSDKPrefersStoreLatest(t *testing.T) {
	isolateSDKCache(t, "0.3.0", "0.6.0")
	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	t.Cleanup(restore)
	manifest := []byte(`{"version":"0.6.0","artifacts":[{"name":"driverforge-sdk-0.6.0.zip","url":"https://unused.example/x.zip","sha256":"00"}]}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "manifest.json.sig") {
			w.Write(sign(manifest))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "manifest.json") {
			t.Errorf("unexpected fetch: %s", r.URL.Path)
		}
		w.Write(manifest)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

	var out bytes.Buffer
	path, version, cached, err := resolveSDK(context.Background(), &out)
	if err != nil {
		t.Fatalf("resolveSDK: %v", err)
	}
	if version != "0.6.0" || !cached {
		t.Errorf("resolved (%q, cached=%v), want the store's 0.6.0 from cache", version, cached)
	}
	if b, _ := os.ReadFile(path); string(b) != "-- sdk 0.6.0" {
		t.Errorf("path %q doesn't hold the 0.6.0 bundle", path)
	}
}

// Store unreachable: fall back to the newest cached bundle with a visible
// warning, so offline init still works.
func TestResolveSDKOfflineFallsBackToCache(t *testing.T) {
	isolateSDKCache(t, "0.3.0")
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", "http://127.0.0.1:1")

	var out bytes.Buffer
	_, version, _, err := resolveSDK(context.Background(), &out)
	if err != nil {
		t.Fatalf("resolveSDK: %v", err)
	}
	if version != "0.3.0" {
		t.Errorf("version = %q, want the cached 0.3.0", version)
	}
	if !strings.Contains(out.String(), "couldn't reach the release store") {
		t.Errorf("offline fallback must warn:\n%s", out.String())
	}
}

// Store unreachable and nothing cached: a friendly error, not a crash.
func TestResolveSDKOfflineNoCacheErrors(t *testing.T) {
	isolateSDKCache(t)
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", "http://127.0.0.1:1")

	var out bytes.Buffer
	if _, _, _, err := resolveSDK(context.Background(), &out); err == nil {
		t.Fatal("no store and no cache should be an error")
	}
}
