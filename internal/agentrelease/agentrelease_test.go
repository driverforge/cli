package agentrelease

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/releaseverify"
	"github.com/driverforge/cli/internal/testzip"
)

func TestArtifactFor(t *testing.T) {
	if got := ArtifactFor("anvil-agent-dev.c4z"); got != "anvil-agent-dev" {
		t.Errorf("artifact = %q, want anvil-agent-dev (profile preserved)", got)
	}
	if got := ArtifactFor("anvil-agent.c4z"); got != "anvil-agent" {
		t.Errorf("artifact = %q, want anvil-agent", got)
	}
}

// zipWith builds a zip holding a single file.
func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Latest reads the store's shared artifacts schema: the download URL and zip
// checksum come from the artifact whose contents carry the installed .c4z.
func TestLatestResolvesArtifactsSchema(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(signedManifestHandler(t, `{
			"version": "2026072201",
			"tag": "2026072201",
			"artifacts": [{
				"name": "anvil-agent-dev-2026072201.zip",
				"url": "https://releases.example/anvil-agent-dev/2026072201/anvil-agent-dev-2026072201.zip",
				"sha256": "ABC123",
				"contents": ["anvil-agent-dev.c4z"]
			}]
		}`, func(r *http.Request) { gotPath = r.URL.Path }))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

	rel, err := Latest(context.Background(), "anvil-agent-dev.c4z")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if gotPath != "/anvil-agent-dev/latest/manifest.json" {
		t.Errorf("manifest path = %q (profile not keyed into the artifact)", gotPath)
	}
	if rel.Version != "2026072201" {
		t.Errorf("version = %q", rel.Version)
	}
	if rel.ZipURL != "https://releases.example/anvil-agent-dev/2026072201/anvil-agent-dev-2026072201.zip" {
		t.Errorf("zip url = %q — must come from the manifest, never guessed", rel.ZipURL)
	}
	if rel.SHA256 != "abc123" {
		t.Errorf("sha256 should be lowercased: %q", rel.SHA256)
	}
	if rel.Payload != "anvil-agent-dev.c4z" {
		t.Errorf("payload = %q, want the installed filename", rel.Payload)
	}
}

// The artifact is selected by its contents; a manifest that doesn't carry the
// installed .c4z is refused rather than guessed at.
func TestLatestArtifactSelection(t *testing.T) {
	srv := httptest.NewServer(signedManifestHandler(t, `{
			"version": "1",
			"artifacts": [
				{"name": "other.zip", "url": "https://x/other.zip", "contents": ["other.c4z"]},
				{"name": "agent.zip", "url": "https://x/agent.zip", "contents": ["anvil-agent.c4z"]}
			]
		}`, nil))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

	rel, err := Latest(context.Background(), "anvil-agent.c4z")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.ZipName != "agent.zip" {
		t.Errorf("picked %q, want the artifact whose contents match", rel.ZipName)
	}

	if _, err := Latest(context.Background(), "anvil-agent-dev.c4z"); err == nil {
		t.Error("a manifest without the installed .c4z should error, not guess")
	}
}

// A single artifact that declares no contents is accepted (older manifests).
func TestLatestSoleArtifactWithoutContents(t *testing.T) {
	srv := httptest.NewServer(signedManifestHandler(t, `{"version":"1","artifacts":[{"name":"a.zip","url":"https://x/a.zip"}]}`, nil))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
	rel, err := Latest(context.Background(), "anvil-agent.c4z")
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.ZipURL != "https://x/a.zip" {
		t.Errorf("zip url = %q", rel.ZipURL)
	}
}

func TestLatestMissingVersionErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"artifacts":[{"name":"a.zip","url":"https://x/a.zip"}]}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
	if _, err := Latest(context.Background(), "anvil-agent.c4z"); err == nil {
		t.Fatal("a manifest with no version should error")
	}
}

// Download verifies the ZIP against the manifest checksum, then extracts the
// .c4z and returns the checksum of the EXTRACTED bytes (what gets uploaded).
func TestDownloadVerifiesZipAndExtractsPayload(t *testing.T) {
	payload := "AGENTC4ZBYTES"
	zipData := testzip.WithFile(t, "anvil-agent.c4z", payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "anvil-agent.c4z")
	rel := Release{ZipURL: srv.URL + "/a.zip", SHA256: hexSum(zipData), Payload: "anvil-agent.c4z"}

	got, err := Download(context.Background(), rel, dest)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if got != hexSum([]byte(payload)) {
		t.Errorf("returned checksum %q, want the extracted payload's %q", got, hexSum([]byte(payload)))
	}
	if b, _ := os.ReadFile(dest); string(b) != payload {
		t.Errorf("dest = %q, want the extracted .c4z bytes", b)
	}

	// Wrong zip checksum → error, and nothing lands at dest.
	bad := filepath.Join(t.TempDir(), "bad.c4z")
	relBad := rel
	relBad.SHA256 = strings.Repeat("0", 64)
	if _, err := Download(context.Background(), relBad, bad); err == nil {
		t.Error("a checksum mismatch must error")
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Error("a mismatched download should leave nothing at dest")
	}

	// A zip that doesn't contain the payload → error.
	relMiss := rel
	relMiss.Payload = "anvil-agent-dev.c4z"
	if _, err := Download(context.Background(), relMiss, filepath.Join(t.TempDir(), "x.c4z")); err == nil {
		t.Error("a zip without the installed .c4z must error")
	}
}

// A manifest without a checksum is refused (mandatory verification) — and
// leaves nothing at dest. The break-glass env allows it for the transition.
func TestDownloadNoChecksumRefusedUnlessOverridden(t *testing.T) {
	payload := "bytes"
	zipData := testzip.WithFile(t, "anvil-agent.c4z", payload)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write(zipData)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "a.c4z")
	rel := Release{ZipURL: srv.URL + "/a.zip", Payload: "anvil-agent.c4z"}
	if _, err := Download(context.Background(), rel, dest); err == nil {
		t.Fatal("a missing checksum must be refused")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("a refused download must leave nothing at dest")
	}

	// There is no break-glass: an unsigned or unchecksummed release is
	// refused outright, and no environment variable changes that.
	t.Setenv("DRIVERFORGE_ALLOW_UNVERIFIED_RELEASE", "1")
	if _, err := Download(context.Background(), rel, dest); err == nil {
		t.Fatal("a missing checksum must stay refused — the break-glass is gone")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Error("a refused download must leave nothing at dest")
	}
}

// signedManifestHandler serves a manifest and its detached .sig sibling, the
// way the release store does. onManifest, if set, observes only the manifest
// request — the signature fetch must not clobber what a test captured.
func signedManifestHandler(t *testing.T, manifest string, onManifest func(*http.Request)) http.Handler {
	t.Helper()
	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	t.Cleanup(restore)
	body := []byte(manifest)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			_, _ = w.Write(sign(body))
			return
		}
		if onManifest != nil {
			onManifest(r)
		}
		_, _ = w.Write(body)
	})
}

func TestLatestRequiresASignedManifest(t *testing.T) {
	body := `{"version":"1","artifacts":[{"name":"a.zip","url":"https://x/a.zip","contents":["anvil-agent.c4z"]}]}`

	t.Run("an unsigned manifest is refused", func(t *testing.T) {
		// No .sig route: the store 404s it, which must read as unsigned.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".sig") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = fmt.Fprint(w, body)
		}))
		defer srv.Close()
		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

		_, err := Latest(context.Background(), "anvil-agent.c4z")

		if err == nil || !strings.Contains(err.Error(), "not signed") {
			t.Fatalf("Latest = %v, want a refusal to install unsigned code", err)
		}
	})

	t.Run("a signed manifest resolves", func(t *testing.T) {
		srv := httptest.NewServer(signedManifestHandler(t, body, nil))
		defer srv.Close()
		t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)

		rel, err := Latest(context.Background(), "anvil-agent.c4z")

		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if rel.Version != "1" {
			t.Errorf("version = %q, want %q", rel.Version, "1")
		}
	})
}
