package sdkrelease

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/releaseverify"
	"github.com/driverforge/cli/internal/testzip"
)

func TestResolveConstraint(t *testing.T) {
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", "https://example.test/driverforge-releases")

	tests := []struct {
		name       string
		constraint string
		wantPath   string
		wantUser   bool
	}{
		{"empty selects latest", "", "/anvil-sdk/latest/manifest.json", false},
		{"latest keyword", "latest", "/anvil-sdk/latest/manifest.json", false},
		{"exact pins version", "1.2.3", "/anvil-sdk/v1.2.3/manifest.json", false},
		{"partial major rejected", "1", "", true},
		{"partial minor rejected", "1.1", "", true},
		{"garbage rejected", "banana", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveConstraint(tt.constraint)
			if tt.wantUser {
				if !clierr.IsUser(err) {
					t.Fatalf("want UserError, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.HasSuffix(got, tt.wantPath) {
				t.Fatalf("manifest URL = %q, want suffix %q", got, tt.wantPath)
			}
		})
	}
}

// makeZip returns a zip wrapping a single anvil-sdk.lua with the given body.
func TestEnsureCachedDownloadsThenServesFromCache(t *testing.T) {
	const version = "1.0.0"
	const luaBody = "-- driverforge sdk bundle\nAnvil = {}\n"

	var manifestHits, zipHits int32
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()

	zipBytes := testzip.WithFile(t, bundleName, luaBody)
	sum := sha256.Sum256(zipBytes)
	zipSHA := hex.EncodeToString(sum[:])

	zipPath := "/anvil-sdk/v" + version + "/anvil-sdk-" + version + ".zip"

	// Marshalled once so the same bytes are both served and signed — a
	// re-encode between the two would reorder keys and break the signature.
	manifestBody, err := json.Marshal(map[string]any{
		"version": version,
		"artifacts": []map[string]any{{
			"name":   "anvil-sdk-" + version + ".zip",
			"url":    srv.URL + zipPath,
			"sha256": zipSHA,
		}},
	})
	if err != nil {
		t.Fatalf("building manifest: %v", err)
	}
	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	defer restore()

	manifestPath := "/anvil-sdk/v" + version + "/manifest.json"
	mux.HandleFunc(manifestPath, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&manifestHits, 1)
		_, _ = w.Write(manifestBody)
	})
	mux.HandleFunc(manifestPath+".sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sign(manifestBody))
	})
	mux.HandleFunc(zipPath, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&zipHits, 1)
		_, _ = w.Write(zipBytes)
	})

	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
	cfg := t.TempDir()
	t.Setenv("HOME", cfg)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfg, "xdg"))

	// First call: downloads and caches.
	path, gotVer, err := EnsureCached(context.Background(), version)
	if err != nil {
		t.Fatalf("EnsureCached: %v", err)
	}
	if gotVer != version {
		t.Fatalf("version = %q, want %q", gotVer, version)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading cached bundle: %v", err)
	}
	if string(got) != luaBody {
		t.Fatalf("cached bundle = %q, want %q", got, luaBody)
	}
	if zipHits != 1 {
		t.Fatalf("zip fetched %d times, want 1", zipHits)
	}

	// Second call for the same exact version: served from cache, no network.
	mhBefore, zhBefore := atomic.LoadInt32(&manifestHits), atomic.LoadInt32(&zipHits)
	path2, _, err := EnsureCached(context.Background(), version)
	if err != nil {
		t.Fatalf("EnsureCached (cached): %v", err)
	}
	if path2 != path {
		t.Fatalf("cache path changed: %q vs %q", path2, path)
	}
	if atomic.LoadInt32(&manifestHits) != mhBefore || atomic.LoadInt32(&zipHits) != zhBefore {
		t.Fatalf("cache hit still hit the network (manifest %d→%d, zip %d→%d)",
			mhBefore, manifestHits, zhBefore, zipHits)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "0.2.0", 1},
		{"0.2.0", "0.10.0", -1}, // numeric, not lexical
		{"1.2.3", "1.2.3", 0},
		{"1.2.0", "1.2", 0},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCacheInspection(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("HOME", cfg)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfg, "xdg"))

	// Empty cache: nothing reported.
	if IsCached("1.0.0") {
		t.Error("nothing should be cached yet")
	}
	if _, _, ok, err := NewestCached(); err != nil || ok {
		t.Errorf("empty cache: ok=%v err=%v", ok, err)
	}

	// Seed two versions.
	for _, v := range []string{"0.9.0", "0.10.0"} {
		p, _ := CachedPath(v)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("-- "+v), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if !IsCached("0.10.0") {
		t.Error("0.10.0 should be cached")
	}
	v, p, ok, err := NewestCached()
	if err != nil || !ok {
		t.Fatalf("NewestCached: ok=%v err=%v", ok, err)
	}
	if v != "0.10.0" { // numeric compare: 10 > 9
		t.Errorf("newest = %q, want 0.10.0", v)
	}
	if filepath.Base(filepath.Dir(p)) != "0.10.0" {
		t.Errorf("newest path = %q", p)
	}
}

func TestResolveMissingVersionIsUserError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

	_, err := Resolve(context.Background(), "9.9.9")
	if !clierr.IsUser(err) {
		t.Fatalf("want UserError for missing version, got %v", err)
	}
}

func TestExtractBundleRejectsZipWithoutLua(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("README.md")
	_, _ = w.Write([]byte("hi"))
	_ = zw.Close()

	if _, err := extractBundle(buf.Bytes()); err == nil {
		t.Fatal("want error for zip without anvil-sdk.lua")
	}
	if _, err := extractBundle([]byte("not a zip")); err == nil {
		t.Fatal(fmt.Sprint("want error for non-zip data"))
	}
}

// The SDK download is refused when the zip doesn't match the manifest checksum,
// and when the manifest carries no checksum at all (mandatory verification).
func TestEnsureCachedRejectsBadOrMissingChecksum(t *testing.T) {
	const version = "2.0.0"
	zipBytes := testzip.WithFile(t, bundleName, "-- sdk\n")

	serve := func(t *testing.T, sha string) error {
		mux := http.NewServeMux()
		srv := httptest.NewServer(mux)
		defer srv.Close()
		zp := "/anvil-sdk/v" + version + "/sdk.zip"
		art := map[string]any{"name": "sdk.zip", "url": srv.URL + zp}
		if sha != "" {
			art["sha256"] = sha
		}
		manifestBody, err := json.Marshal(map[string]any{"version": version, "artifacts": []map[string]any{art}})
		if err != nil {
			t.Fatalf("building manifest: %v", err)
		}
		sign, restore := releaseverify.SigningFixtureForTesting("test-key")
		defer restore()
		releaseverify.ServeSignedManifestForTesting(mux, "/anvil-sdk/v"+version+"/manifest.json", manifestBody, sign)
		mux.HandleFunc(zp, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(zipBytes) })

		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
		cfg := t.TempDir()
		t.Setenv("HOME", cfg)
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(cfg, "xdg"))
		_, _, err = EnsureCached(context.Background(), version)
		return err
	}

	if err := serve(t, strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("bad checksum should be refused, got %v", err)
	}
	if err := serve(t, ""); err == nil || !strings.Contains(err.Error(), "no sha256 checksum") {
		t.Errorf("missing checksum should be refused, got %v", err)
	}
}

func TestResolveRequiresASignedManifest(t *testing.T) {
	const version = "1.0.0"
	body := []byte(`{"version":"1.0.0","artifacts":[{"name":"a.zip","url":"https://x/a.zip","sha256":"00"}]}`)

	newStore := func(t *testing.T, manifest []byte, serveSig bool) string {
		t.Helper()
		sign, restore := releaseverify.SigningFixtureForTesting("test-key")
		t.Cleanup(restore)
		mux := http.NewServeMux()
		path := "/anvil-sdk/v" + version + "/manifest.json"
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(manifest) })
		if serveSig {
			mux.HandleFunc(path+".sig", func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write(sign(manifest))
			})
		}
		srv := httptest.NewServer(mux)
		t.Cleanup(srv.Close)
		return srv.URL
	}

	t.Run("an unsigned manifest is refused", func(t *testing.T) {
		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", newStore(t, body, false))

		_, err := Resolve(context.Background(), version)

		if err == nil || !strings.Contains(err.Error(), "not signed") {
			t.Fatalf("Resolve = %v, want a refusal to install unsigned code", err)
		}
	})

	t.Run("a signed manifest resolves", func(t *testing.T) {
		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", newStore(t, body, true))

		rel, err := Resolve(context.Background(), version)

		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if rel.Version != version {
			t.Errorf("version = %q, want %q", rel.Version, version)
		}
	})

	t.Run("an unsigned manifest is refused before it is parsed", func(t *testing.T) {
		// Malformed JSON with no signature. If the error mentions decoding, we
		// parsed attacker-controlled bytes before establishing they were ours.
		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", newStore(t, []byte("{not json"), false))

		_, err := Resolve(context.Background(), version)

		if err == nil {
			t.Fatal("expected a refusal")
		}
		if strings.Contains(err.Error(), "decoding manifest") {
			t.Fatalf("manifest was parsed before its signature was checked: %v", err)
		}
		if !strings.Contains(err.Error(), "not signed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}
