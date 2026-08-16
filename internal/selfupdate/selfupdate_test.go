package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/releaseverify"
)

// This package replaces the binary the user runs, so two properties carry the
// weight: nothing unverified is ever installed, and a refusal leaves the
// existing binary exactly as it was. The tests below drive the real Apply
// against a signed fixture store and assert both on every way the chain can
// break.

const fixtureVersion = "9.9.9"

// newBinary is the content of the "upgrade" being installed.
var newBinary = []byte("#!/bin/sh\necho upgraded\n")

// oldBinary stands in for the CLI currently on disk.
var oldBinary = []byte("#!/bin/sh\necho original\n")

type fixture struct {
	manifest []byte
	archive  []byte
	assetURL string
	// tamper mutates the served bytes after signing.
	breakArchive  bool
	breakManifest bool
	unsigned      bool
	unknownKey    bool
}

// serve stands up a release store shaped like the real one and returns the
// manifest URL to point Apply at.
func (f *fixture) serve(t *testing.T) string {
	t.Helper()

	sign, restore := releaseverify.SigningFixtureForTesting("release-signing-v1")
	t.Cleanup(restore)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	asset := AssetName(fixtureVersion, runtime.GOOS, runtime.GOARCH)
	f.archive = tarGz(t, binaryName, newBinary)
	sum := sha256.Sum256(f.archive)

	// The SBOM is listed FIRST and shares the archive's name plus a suffix. An
	// artifact lookup matching on prefix would take this entry, then fail the
	// checksum — so the happy path passing is what proves the two stay
	// distinguishable.
	f.manifest = []byte(fmt.Sprintf(`{
  "version": %q,
  "tag": "v%s",
  "artifacts": [
    {"name": "%s.sbom.json", "url": "%s/%s.sbom.json", "sha256": "%s"},
    {"name": "%s", "url": "%s/%s", "sha256": "%s"}
  ]
}`, fixtureVersion, fixtureVersion,
		asset, srv.URL, asset, strings.Repeat("b", 64),
		asset, srv.URL, asset, hex.EncodeToString(sum[:])))

	sidecar := sign(f.manifest)
	served := f.manifest

	if f.breakManifest {
		// Signed, then altered: the bytes no longer match the signature.
		served = bytes.Replace(served, []byte(`"version": "9.9.9"`), []byte(`"version": "9.9.8"`), 1)
	}
	if f.unknownKey {
		sidecar = bytes.Replace(sidecar, []byte(`"release-signing-v1"`), []byte(`"release-signing-v9"`), 1)
	}

	mux.HandleFunc("/manifest.json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(served)
	})
	mux.HandleFunc("/manifest.json.sig", func(w http.ResponseWriter, _ *http.Request) {
		if f.unsigned {
			http.NotFound(w, &http.Request{})
			return
		}
		_, _ = w.Write(sidecar)
	})
	mux.HandleFunc("/"+asset, func(w http.ResponseWriter, _ *http.Request) {
		body := f.archive
		if f.breakArchive {
			body = append(append([]byte{}, body...), "evil"...)
		}
		_, _ = w.Write(body)
	})

	return srv.URL + "/manifest.json"
}

// installedBinary writes a stand-in for the running CLI and returns a Target
// pointing at it.
func installedBinary(t *testing.T) Target {
	t.Helper()
	path := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(path, oldBinary, 0o755); err != nil {
		t.Fatalf("writing the installed binary: %v", err)
	}
	return Target{Path: path, Writable: true}
}

func TestApplyInstallsAVerifiedRelease(t *testing.T) {
	f := &fixture{}
	url := f.serve(t)
	target := installedBinary(t)

	got, err := Apply(context.Background(), url, target, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got != fixtureVersion {
		t.Errorf("installed version = %q, want %q", got, fixtureVersion)
	}

	on, err := os.ReadFile(target.Path)
	if err != nil {
		t.Fatalf("reading the installed binary: %v", err)
	}
	if !bytes.Equal(on, newBinary) {
		t.Errorf("binary was not replaced\n got: %q\nwant: %q", on, newBinary)
	}
	fi, err := os.Stat(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("installed binary is not executable (mode %v)", fi.Mode().Perm())
	}
}

// Each way of breaking the chain must refuse, and — the property that matters
// most — must leave the working binary untouched.
func TestApplyRefusesAndLeavesTheBinaryIntact(t *testing.T) {
	for _, c := range []struct {
		name    string
		fix     fixture
		wantErr string
	}{
		{"tampered archive", fixture{breakArchive: true}, "checksum mismatch"},
		{"tampered manifest", fixture{breakManifest: true}, "signature is invalid"},
		{"unsigned manifest", fixture{unsigned: true}, "not signed"},
		{"unknown signing key", fixture{unknownKey: true}, "does not trust"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := c.fix
			url := f.serve(t)
			target := installedBinary(t)

			_, err := Apply(context.Background(), url, target, nil)
			if err == nil {
				t.Fatal("Apply succeeded but the release should have been refused")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error did not explain itself: want %q in %q", c.wantErr, err)
			}

			on, readErr := os.ReadFile(target.Path)
			if readErr != nil {
				t.Fatalf("the installed binary is gone after a refused upgrade: %v", readErr)
			}
			if !bytes.Equal(on, oldBinary) {
				t.Errorf("a refused upgrade modified the installed binary\ngot: %q", on)
			}
		})
	}
}

// A refusal must not leave staging files next to the binary either — in a
// Homebrew Caskroom or ~/.local/bin those would accumulate silently.
func TestApplyLeavesNoTemporaryFilesBehind(t *testing.T) {
	f := &fixture{breakArchive: true}
	url := f.serve(t)
	target := installedBinary(t)

	if _, err := Apply(context.Background(), url, target, nil); err == nil {
		t.Fatal("expected the tampered archive to be refused")
	}

	entries, err := os.ReadDir(filepath.Dir(target.Path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".driverforge-update") {
			t.Errorf("left a staging file behind: %s", e.Name())
		}
	}
}

func TestAssetNameMatchesTheReleaseArchives(t *testing.T) {
	for _, c := range []struct{ goos, goarch, want string }{
		{"darwin", "arm64", "driverforge_1.2.3_darwin_arm64.tar.gz"},
		{"linux", "amd64", "driverforge_1.2.3_linux_amd64.tar.gz"},
		{"windows", "amd64", "driverforge_1.2.3_windows_amd64.zip"},
	} {
		if got := AssetName("1.2.3", c.goos, c.goarch); got != c.want {
			t.Errorf("AssetName(1.2.3, %s, %s) = %q, want %q", c.goos, c.goarch, got, c.want)
		}
	}
}

// The manifest lists an SBOM per archive, named by appending to the archive's
// own name. Selecting by prefix or suffix would pick the wrong entry.
func TestPickArtifactMatchesExactlyNotByPrefix(t *testing.T) {
	want := "driverforge_1.2.3_linux_amd64.tar.gz"
	artifacts := []artifactDoc{
		{Name: want + ".sbom.json", URL: "u", SHA256: "sbom"},
		{Name: want, URL: "u", SHA256: "archive"},
	}
	got, err := pickArtifact(artifacts, want)
	if err != nil {
		t.Fatalf("pickArtifact: %v", err)
	}
	if got.SHA256 != "archive" {
		t.Errorf("selected the wrong artifact: got checksum %q, want %q", got.SHA256, "archive")
	}
}

func TestPickArtifactReportsAnUnpublishedPlatform(t *testing.T) {
	_, err := pickArtifact([]artifactDoc{{Name: "other.tar.gz"}}, "driverforge_1.2.3_plan9_arm.tar.gz")
	if err == nil {
		t.Fatal("expected an error for a platform the release does not carry")
	}
	if !strings.Contains(err.Error(), "plan9") {
		t.Errorf("error should name the missing asset, got %q", err)
	}
}

// Homebrew puts a symlink on PATH and the real binary in the Caskroom. The
// upgrade has to land on the real file, or it would replace brew's symlink
// with a regular file and break its bookkeeping.
func TestLocateResolvesThroughASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink layout is the Homebrew/Unix case")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real-driverforge")
	if err := os.WriteFile(real, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "driverforge")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	resolved, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != real {
		// Guards the assumption Locate relies on, without being able to set
		// os.Executable() for this process.
		realResolved, _ := filepath.EvalSymlinks(real)
		if resolved != realResolved {
			t.Errorf("EvalSymlinks(%s) = %s, want %s", link, resolved, real)
		}
	}
}

// replaceBinary keeps the mode the package manager set. A cask-installed binary
// that came back 0600 would stop being runnable.
func TestReplaceBinaryPreservesPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(path, oldBinary, 0o751); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(path, newBinary); err != nil {
		t.Fatalf("replaceBinary: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o751 {
		t.Errorf("mode = %v, want 0751", fi.Mode().Perm())
	}
}

func TestReplaceBinaryRefusesEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), binaryName)
	if err := os.WriteFile(path, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(path, nil); err == nil {
		t.Fatal("expected replaceBinary to refuse empty content")
	}
	on, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(on, oldBinary) {
		t.Error("refusing empty content must leave the binary untouched")
	}
}

func TestExtractBinaryRejectsAnArchiveWithoutTheBinary(t *testing.T) {
	archive := tarGz(t, "README.md", []byte("not a binary"))
	if _, err := extractBinary(archive, "x.tar.gz"); err == nil {
		t.Fatal("expected extraction to fail when the archive has no driverforge")
	}
}

func tarGz(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
