// Package agentrelease discovers and downloads Anvil Agent builds from the same
// release store the CLI's own update check uses
// (https://releases.driverforge.com/driverforge-releases/<artifact>/latest/manifest.json).
//
// The manifest is the store's shared schema — version/tag/commit/releasedAt
// plus an artifacts[] array whose entries carry the versioned download URL, the
// zip's sha256, and the files the zip contains. The agent artifact is a zip
// wrapping the installable .c4z (anvil-agent-<version>.zip → anvil-agent.c4z),
// so a download verifies the zip and extracts the .c4z from it.
//
// The artifact path is derived from the agent's *installed* filename so the
// download is keyed by environment profile: an installed anvil-agent-dev.c4z
// resolves the "anvil-agent-dev" artifact, never the prod build. This guards
// against silently switching a controller's environment on upgrade (the agent
// bakes its broker/OAuth/graph URLs in per profile).
//
// The Driverforge Agent still ships under its original "anvil-agent" name (it is a
// separate repo with its own release pipeline); only the store root moved to
// driverforge-releases in the CLI rename.
package agentrelease

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/releaseverify"
)

const defaultBaseURL = "https://releases.driverforge.com/driverforge-releases"

// downloadClient has a generous timeout — the agent zip is small (a .c4z of
// hundreds of KB) but the manifest fetch and the blob fetch share it.
var downloadClient = &http.Client{Timeout: 60 * time.Second}

// maxDownload bounds the zip read — the agent artifact is well under a
// megabyte; this guards against a runaway body.
const maxDownload = 64 << 20

// Release is a resolved agent build available to download.
type Release struct {
	Artifact string // e.g. "anvil-agent-dev" (carries the env profile)
	Version  string // latest version string from the manifest
	ZipURL   string // versioned download URL of the artifact zip
	ZipName  string // zip filename (informational, for messages)
	SHA256   string // hex-encoded expected checksum of the ZIP ("" if the manifest omits it)
	Payload  string // the .c4z inside the zip to extract (the installed filename)
}

// baseURL is the release store root, overridable for testing / self-hosting.
func baseURL() string {
	if u := os.Getenv("DRIVERFORGE_RELEASES_BASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultBaseURL
}

// ArtifactFor maps an installed agent filename to its release artifact name by
// dropping the .c4z suffix (anvil-agent-dev.c4z → anvil-agent-dev).
func ArtifactFor(installedFilename string) string {
	return strings.TrimSuffix(strings.TrimSpace(installedFilename), ".c4z")
}

// manifestDoc is the release store's shared manifest schema (the fields this
// package needs of it).
type manifestDoc struct {
	Version   string        `json:"version"`
	Artifacts []artifactDoc `json:"artifacts"`
}

type artifactDoc struct {
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	SHA256   string   `json:"sha256"`
	Contents []string `json:"contents"`
}

// Latest resolves the newest agent build for the given installed filename. It
// errors on a missing/empty filename, an unreachable/invalid manifest, or a
// manifest whose artifacts don't carry that file; the caller compares Version
// against what's running to decide whether to upgrade.
func Latest(ctx context.Context, installedFilename string) (Release, error) {
	installedFilename = strings.TrimSpace(installedFilename)
	artifact := ArtifactFor(installedFilename)
	if artifact == "" {
		return Release{}, fmt.Errorf("agentrelease: no agent filename to resolve a release for")
	}
	m, err := fetchManifest(ctx, baseURL()+"/"+artifact+"/latest/manifest.json")
	if err != nil {
		return Release{}, err
	}
	a, err := pickArtifact(m.Artifacts, installedFilename)
	if err != nil {
		return Release{}, err
	}
	if a.URL == "" {
		return Release{}, fmt.Errorf("agentrelease: manifest artifact %s has no download url", a.Name)
	}
	return Release{
		Artifact: artifact,
		Version:  m.Version,
		ZipURL:   a.URL,
		ZipName:  a.Name,
		SHA256:   strings.ToLower(strings.TrimSpace(a.SHA256)),
		Payload:  installedFilename,
	}, nil
}

// fetchManifest gets and decodes the latest-release manifest, requiring a
// version.
func fetchManifest(ctx context.Context, manifestURL string) (manifestDoc, error) {
	var m manifestDoc
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return m, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return m, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("agentrelease: manifest %s: HTTP %d", manifestURL, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return m, fmt.Errorf("agentrelease: reading manifest: %w", err)
	}

	// Verify before parsing — see sdkrelease.Resolve.
	signature, err := releaseverify.FetchSignature(ctx, downloadClient, manifestURL)
	if err != nil {
		return m, err
	}
	if err := releaseverify.VerifyManifest("Anvil Agent", raw, signature); err != nil {
		return m, err
	}

	if err := json.Unmarshal(raw, &m); err != nil {
		return m, fmt.Errorf("agentrelease: decoding manifest: %w", err)
	}
	if m.Version == "" {
		return m, fmt.Errorf("agentrelease: manifest missing version")
	}
	return m, nil
}

// pickArtifact chooses the manifest artifact that carries the installed .c4z:
// the one whose contents list it, or — when no artifact declares contents —
// the sole artifact. Anything else is a manifest this build doesn't understand.
func pickArtifact(artifacts []artifactDoc, installedFilename string) (artifactDoc, error) {
	for _, a := range artifacts {
		for _, c := range a.Contents {
			if c == installedFilename {
				return a, nil
			}
		}
	}
	if len(artifacts) == 1 && len(artifacts[0].Contents) == 0 {
		return artifacts[0], nil
	}
	return artifactDoc{}, fmt.Errorf("agentrelease: manifest has no artifact carrying %s", installedFilename)
}

// Download fetches the release zip, verifies it against the manifest's SHA-256
// (mandatory — see internal/releaseverify), extracts the installable .c4z, and
// writes it to dest atomically (temp file + rename) so a partial or rejected
// download never lands there. It returns the hex checksum of the EXTRACTED
// .c4z — the bytes the caller uploads — so it can be forwarded to the agent as
// X-Driver-SHA256.
func Download(ctx context.Context, rel Release, dest string) (sha string, err error) {
	raw, err := fetchZip(ctx, rel.ZipURL)
	if err != nil {
		return "", err
	}

	// Verify the zip against the manifest checksum before trusting its
	// contents — this code runs on the user's controller.
	sum := sha256.Sum256(raw)
	if err := releaseverify.Verify("Anvil Agent", hex.EncodeToString(sum[:]), rel.SHA256); err != nil {
		return "", err
	}

	c4z, err := extractPayload(raw, rel.Payload)
	if err != nil {
		return "", fmt.Errorf("agentrelease: %s: %w", rel.ZipName, err)
	}
	if err := writeAtomic(dest, c4z); err != nil {
		return "", err
	}
	payloadSum := sha256.Sum256(c4z)
	return hex.EncodeToString(payloadSum[:]), nil
}

// fetchZip gets the artifact zip into memory (bounded by maxDownload).
func fetchZip(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agentrelease: download %s: HTTP %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxDownload))
	if err != nil {
		return nil, fmt.Errorf("agentrelease: reading download: %w", err)
	}
	return raw, nil
}

// writeAtomic writes data to dest via a temp file + rename, so a partial write
// never lands at dest.
func writeAtomic(dest string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".anvil-agent-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed
	_, writeErr := tmp.Write(data)
	if closeErr := tmp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return os.Rename(tmpName, dest)
}

// extractPayload pulls the installable .c4z out of the release zip.
func extractPayload(zipData []byte, payload string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("not a valid zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != payload {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, maxDownload))
	}
	return nil, fmt.Errorf("zip does not contain %s", payload)
}
