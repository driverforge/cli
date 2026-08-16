// Package selfupdate replaces the running driverforge binary with the latest
// release.
//
// It deliberately reuses the release trust chain rather than inventing one:
// the manifest's detached signature is verified against the key compiled into
// this binary, and the downloaded archive is checked against the sha256 that
// signed manifest declares (internal/releaseverify). That is the same chain
// scripts/install.sh runs in shell, so a binary that upgrades itself is held
// to exactly the policy a fresh install is — and a stronger one than the
// package managers offer, which pin a checksum inside a public tap rather than
// verifying a signature.
//
// Replacing the binary is gated on being able to write to the directory
// holding it. Where we cannot (a root-owned prefix, a shared multi-user
// install), that is not an error: the caller falls back to printing the
// package manager's upgrade command, which is what the CLI did for every
// install before this existed.
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/releaseverify"
)

// binaryName is the executable this package installs, without any platform
// extension.
const binaryName = "driverforge"

// maxDownload bounds an archive read so a hostile origin cannot stream us an
// unbounded body. Release archives are single-digit megabytes.
const maxDownload = 128 << 20

// artifactLabel names this download in verification failures, which surface to
// the user verbatim.
const artifactLabel = "driverforge CLI"

var downloadClient = &http.Client{Timeout: 5 * time.Minute}

// Target is the binary this process would replace, and whether it can.
type Target struct {
	// Path is the running executable with symlinks resolved — a Homebrew cask
	// puts a symlink on PATH and the real file in the Caskroom, and it is the
	// real file that has to be replaced.
	Path string
	// Writable reports whether Path can actually be swapped. See Locate.
	Writable bool
	// Reason explains a false Writable, for the caller's fallback message.
	Reason string
}

// Locate resolves the running binary and reports whether this process could
// replace it.
//
// Writability is established by creating and removing a real temp file in the
// target's directory rather than by reading permission bits. Replacing the
// binary is a rename into that directory, so directory write permission — not
// the file's own mode — is what governs, and mode bits alone would miss
// read-only mounts, immutable flags and ACLs.
func Locate() (Target, error) {
	exe, err := os.Executable()
	if err != nil {
		return Target{}, fmt.Errorf("selfupdate: locating the running binary: %w", err)
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}

	t := Target{Path: exe}
	probe, err := os.CreateTemp(filepath.Dir(exe), ".driverforge-update-probe-*")
	if err != nil {
		t.Reason = "the directory holding it is not writable by this user"
		return t, nil
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)

	t.Writable = true
	return t, nil
}

// manifestDoc is the release store's manifest, narrowed to what an upgrade
// needs.
type manifestDoc struct {
	Version   string        `json:"version"`
	Artifacts []artifactDoc `json:"artifacts"`
}

type artifactDoc struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// Apply downloads the release the manifest at manifestURL describes and
// replaces target's binary with it. It reports the version installed.
//
// progress, if non-nil, is called with short human-readable step labels.
func Apply(ctx context.Context, manifestURL string, target Target, progress func(string)) (string, error) {
	step := func(s string) {
		if progress != nil {
			progress(s)
		}
	}

	step("Checking the signed release manifest")
	m, err := fetchManifest(ctx, manifestURL)
	if err != nil {
		return "", err
	}

	want := AssetName(m.Version, runtime.GOOS, runtime.GOARCH)
	a, err := pickArtifact(m.Artifacts, want)
	if err != nil {
		return "", err
	}

	step("Downloading " + want)
	raw, err := download(ctx, a.URL)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(raw)
	if err := releaseverify.Verify(artifactLabel, hex.EncodeToString(sum[:]), a.SHA256); err != nil {
		return "", err
	}

	step("Verifying and installing")
	bin, err := extractBinary(raw, want)
	if err != nil {
		return "", err
	}
	if err := replaceBinary(target.Path, bin); err != nil {
		return "", err
	}
	return m.Version, nil
}

// AssetName is the release archive for a platform, matching goreleaser's
// name_template. Windows ships a zip, every other platform a tar.gz.
func AssetName(version, goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("%s_%s_%s_%s.%s", binaryName, version, goos, goarch, ext)
}

// fetchManifest retrieves the manifest and its detached signature, verifying
// the signature over the exact bytes served before parsing them. Verifying
// first is deliberate: a re-encoding of a parsed document would not match the
// signature, and nothing from an unverified manifest should be acted on.
func fetchManifest(ctx context.Context, manifestURL string) (manifestDoc, error) {
	var m manifestDoc

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return m, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return m, fmt.Errorf("selfupdate: fetching the release manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("selfupdate: release manifest %s: HTTP %d", manifestURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return m, fmt.Errorf("selfupdate: reading the release manifest: %w", err)
	}

	signature, err := releaseverify.FetchSignature(ctx, downloadClient, manifestURL)
	if err != nil {
		return m, err
	}
	if err := releaseverify.VerifyManifest(artifactLabel, raw, signature); err != nil {
		return m, err
	}

	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("selfupdate: decoding the release manifest: %w", err)
	}
	if m.Version == "" {
		return m, fmt.Errorf("selfupdate: release manifest has no version")
	}
	return m, nil
}

// pickArtifact finds the archive for this platform by exact name. The match is
// exact rather than a prefix or suffix test because the manifest also lists an
// SBOM per archive, named by appending to the archive's own name — a prefix
// match would select it and then fail the checksum.
func pickArtifact(artifacts []artifactDoc, want string) (artifactDoc, error) {
	for _, a := range artifacts {
		if a.Name != want {
			continue
		}
		if a.URL == "" {
			return artifactDoc{}, fmt.Errorf("selfupdate: manifest entry %s has no download url", want)
		}
		return a, nil
	}
	return artifactDoc{}, fmt.Errorf("selfupdate: this release has no %s — the platform may not be published yet", want)
}

func download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("selfupdate: downloading the release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("selfupdate: downloading %s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: reading the download: %w", err)
	}
	return raw, nil
}

// extractBinary pulls the driverforge executable out of a verified archive.
// The archive has already been checked against the signed manifest, so this
// reads trusted bytes; it still refuses anything that does not contain exactly
// the expected entry rather than installing whatever it finds.
func extractBinary(raw []byte, assetName string) ([]byte, error) {
	if strings.HasSuffix(assetName, ".zip") {
		return extractFromZip(raw)
	}
	return extractFromTarGz(raw)
}

func extractFromTarGz(raw []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: opening the release archive: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("selfupdate: reading the release archive: %w", err)
		}
		if filepath.Base(h.Name) != binaryName || h.Typeflag != tar.TypeReg {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, maxDownload))
	}
	return nil, fmt.Errorf("selfupdate: the release archive contains no %s", binaryName)
}

func extractFromZip(raw []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("selfupdate: opening the release archive: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != binaryName+".exe" && filepath.Base(f.Name) != binaryName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("selfupdate: reading the release archive: %w", err)
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxDownload))
	}
	return nil, fmt.Errorf("selfupdate: the release archive contains no %s", binaryName)
}

// replaceBinary swaps path's contents for data.
//
// The new binary is staged as a temp file in the same directory so the final
// step is a rename within one filesystem: either the old binary or the new one
// is in place, never a half-written file. On Unix this works on the running
// executable because the rename replaces the directory entry while this
// process keeps its open inode. Windows will not rename over a running image,
// so the old one is moved aside first and swept up on a later run.
func replaceBinary(path string, data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("selfupdate: refusing to install an empty binary")
	}
	dir := filepath.Dir(path)

	mode := os.FileMode(0o755)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	tmp, err := os.CreateTemp(dir, ".driverforge-update-*")
	if err != nil {
		return fmt.Errorf("selfupdate: staging the new binary: %w", err)
	}
	staged := tmp.Name()
	defer os.Remove(staged) // no-op once the rename succeeds

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("selfupdate: writing the new binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("selfupdate: writing the new binary: %w", err)
	}
	if err := os.Chmod(staged, mode); err != nil {
		return fmt.Errorf("selfupdate: setting permissions on the new binary: %w", err)
	}

	if runtime.GOOS == "windows" {
		// Renaming the running image out of the way is permitted where
		// replacing it is not. If the sweep fails it is because the old image
		// is still mapped; the next run removes it.
		old := path + ".old"
		_ = os.Remove(old)
		if err := os.Rename(path, old); err != nil {
			return fmt.Errorf("selfupdate: moving the running binary aside: %w", err)
		}
		if err := os.Rename(staged, path); err != nil {
			_ = os.Rename(old, path) // put the working binary back
			return fmt.Errorf("selfupdate: installing the new binary: %w", err)
		}
		_ = os.Remove(old)
		return nil
	}

	if err := os.Rename(staged, path); err != nil {
		return fmt.Errorf("selfupdate: installing the new binary: %w", err)
	}
	return nil
}

// SweepOldBinary removes the previous executable left behind by a Windows
// self-update. It is best-effort and silent: the file is only present after an
// upgrade, and only until the first run that can delete it.
func SweepOldBinary() {
	if runtime.GOOS != "windows" {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	_ = os.Remove(exe + ".old")
}
