// Package sdkrelease resolves and caches the Driverforge SDK bundle (driverforge-sdk.lua)
// that `driverforge init` embeds into a driver's source tree.
//
// The SDK is published to the same release store the agent uses
// (https://releases.driverforge.com/driverforge-releases/driverforge-sdk/<v|latest>/), as a
// zip wrapping a single driverforge-sdk.lua, alongside a manifest.json. Resolved
// bundles are cached per-version under the user config dir so repeat inits and
// re-runs don't re-download.
//
// Version selection is deliberately limited for now: an empty constraint
// resolves "latest"; a full x.y.z pins that exact build. Partial constraints
// (e.g. "1" or "1.1") are rejected with a friendly error until the release
// store publishes a versions index to match them against — see ResolveConstraint.
package sdkrelease

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/releaseverify"

	"github.com/driverforge/cli/internal/clierr"
)

// defaultBaseURL is the release-store root; the SDK lives under <root>/driverforge-sdk.
// It shares the driverforge-releases namespace (and override env) with agentrelease.
const defaultBaseURL = "https://releases.driverforge.com/driverforge-releases"

// artifactPath is the SDK's directory within the release store. It follows the
// SDK's own rename (DF-915): the publish pipeline writes under driverforge-sdk,
// and the bundle init embeds is vendor/driverforge-sdk.lua.
const artifactPath = "driverforge-sdk"

// BundleName is the Lua file inside each release zip, the cached filename, and
// the name init embeds into a project's src/vendor/. One definition: a second
// spelling would let the writer and the reader silently disagree about which
// file is "the SDK".
const BundleName = "driverforge-sdk.lua"

// downloadClient bounds the manifest + zip fetches; the bundle is tens of KB.
var downloadClient = &http.Client{Timeout: 60 * time.Second}

var (
	exactRe   = regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	partialRe = regexp.MustCompile(`^\d+(\.\d+)?$`)
)

// Release is a resolved SDK build available to download.
type Release struct {
	Version string // concrete version, e.g. "1.0.0"
	ZipURL  string // download URL of the zip wrapping driverforge-sdk.lua
	ZipName string // zip filename (informational)
	SHA256  string // hex sha256 of the zip, from the manifest ("" if absent)
}

// baseURL is the release-store root, overridable for testing / self-hosting.
func baseURL() string {
	if u := os.Getenv("DRIVERFORGE_RELEASES_BASE_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return defaultBaseURL
}

// manifestDoc is the SDK release manifest produced by the release workflow.
type manifestDoc struct {
	Version   string `json:"version"`
	Artifacts []struct {
		Name   string `json:"name"`
		URL    string `json:"url"`
		SHA256 string `json:"sha256"`
	} `json:"artifacts"`
}

// ResolveConstraint maps a user-supplied --sdk-version constraint to the release
// store directory holding its manifest. An empty constraint selects "latest"; a
// full x.y.z selects that immutable version. Partial constraints are a usage
// error for now (no versions index exists to resolve them against).
func ResolveConstraint(constraint string) (manifestURL string, err error) {
	c := strings.TrimSpace(constraint)
	switch {
	case c == "" || c == "latest":
		return baseURL() + "/" + artifactPath + "/latest/manifest.json", nil
	case exactRe.MatchString(c):
		return baseURL() + "/" + artifactPath + "/v" + c + "/manifest.json", nil
	case partialRe.MatchString(c):
		return "", &clierr.UserError{
			Title:   "Pin an exact SDK version",
			Message: fmt.Sprintf("--sdk-version %q is a partial version", c),
			Hint:    "pin a full version like 1.0.0, or omit --sdk-version for the latest",
		}
	default:
		return "", &clierr.UserError{
			Title:   "Invalid SDK version",
			Message: fmt.Sprintf("%q is not a valid version", c),
			Hint:    "use a full version like 1.0.0, or omit --sdk-version for the latest",
		}
	}
}

// Resolve fetches the manifest for the given constraint and returns the concrete
// release to download.
func Resolve(ctx context.Context, constraint string) (Release, error) {
	manifestURL, err := ResolveConstraint(constraint)
	if err != nil {
		return Release{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return Release{}, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return Release{}, &clierr.UserError{
			Title:   "SDK version not found",
			Message: "that Driverforge SDK version isn't published",
			Hint:    "check the version, or omit --sdk-version for the latest",
		}
	}
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("sdkrelease: manifest %s: HTTP %d", manifestURL, resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Release{}, fmt.Errorf("sdkrelease: reading manifest: %w", err)
	}

	// Verify before parsing: the signature covers the bytes as served, and
	// nothing in an unverified manifest should steer what happens next.
	signature, err := releaseverify.FetchSignature(ctx, downloadClient, manifestURL)
	if err != nil {
		return Release{}, err
	}
	if err := releaseverify.VerifyManifest("Driverforge SDK", raw, signature); err != nil {
		return Release{}, err
	}

	var m manifestDoc
	if err := json.Unmarshal(raw, &m); err != nil {
		return Release{}, fmt.Errorf("sdkrelease: decoding manifest: %w", err)
	}
	if m.Version == "" || len(m.Artifacts) == 0 || m.Artifacts[0].URL == "" {
		return Release{}, fmt.Errorf("sdkrelease: manifest %s is missing a version or artifact", manifestURL)
	}

	return Release{
		Version: m.Version,
		ZipURL:  m.Artifacts[0].URL,
		ZipName: m.Artifacts[0].Name,
		SHA256:  m.Artifacts[0].SHA256,
	}, nil
}

// cacheRoot is the per-user SDK cache dir (alongside context.json/credentials.json).
func cacheRoot() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "driverforge", "sdk"), nil
}

// CachedPath returns the on-disk location of a given version's bundle (whether or
// not it exists yet).
func CachedPath(version string) (string, error) {
	root, err := cacheRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, version, BundleName), nil
}

// IsCached reports whether an exact version's bundle is present locally.
func IsCached(version string) bool {
	p, err := CachedPath(version)
	if err != nil {
		return false
	}
	_, statErr := os.Stat(p)
	return statErr == nil
}

// ListCached returns the versions present in the local cache (each with a bundle
// on disk), in no particular order. An absent cache dir yields no versions.
func ListCached() ([]string, error) {
	root, err := cacheRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var versions []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(root, e.Name(), BundleName)); statErr == nil {
			versions = append(versions, e.Name())
		}
	}
	return versions, nil
}

// NewestCached returns the highest-versioned cached bundle and its path. ok is
// false when nothing is cached.
func NewestCached() (version, path string, ok bool, err error) {
	versions, err := ListCached()
	if err != nil {
		return "", "", false, err
	}
	if len(versions) == 0 {
		return "", "", false, nil
	}
	best := versions[0]
	for _, v := range versions[1:] {
		if compareVersions(v, best) > 0 {
			best = v
		}
	}
	p, err := CachedPath(best)
	if err != nil {
		return "", "", false, err
	}
	return best, p, true, nil
}

// compareVersions does a numeric dotted compare (1.10.0 > 1.9.0). Non-numeric
// segments (e.g. pre-release tags) compare as 0 — adequate for picking newest.
func compareVersions(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var ai, bi int
		if i < len(as) {
			ai, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			bi, _ = strconv.Atoi(bs[i])
		}
		if ai != bi {
			if ai < bi {
				return -1
			}
			return 1
		}
	}
	return 0
}

// EnsureCached resolves the constraint and returns the local path to the cached
// driverforge-sdk.lua, downloading and extracting it if absent. For an exact version
// that is already cached it skips the network entirely.
func EnsureCached(ctx context.Context, constraint string) (path, version string, err error) {
	// Fast path: an exact, already-cached version needs no manifest fetch.
	if c := strings.TrimSpace(constraint); exactRe.MatchString(c) {
		if p, perr := CachedPath(c); perr == nil {
			if _, statErr := os.Stat(p); statErr == nil {
				return p, c, nil
			}
		}
	}

	rel, err := Resolve(ctx, constraint)
	if err != nil {
		return "", "", err
	}

	dest, err := CachedPath(rel.Version)
	if err != nil {
		return "", "", err
	}
	if _, statErr := os.Stat(dest); statErr == nil {
		return dest, rel.Version, nil // cache hit
	}

	if err := download(ctx, rel, dest); err != nil {
		return "", "", err
	}
	return dest, rel.Version, nil
}

// download fetches the release zip, extracts driverforge-sdk.lua, and writes it to dest
// atomically (temp file + rename) so a partial download never poisons the cache.
func download(ctx context.Context, rel Release, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rel.ZipURL, nil)
	if err != nil {
		return err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sdkrelease: download %s: HTTP %d", rel.ZipURL, resp.StatusCode)
	}

	// 8 MiB ceiling: the bundle is ~60 KB; this guards against a runaway body.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("sdkrelease: reading download: %w", err)
	}

	// Verify the downloaded zip against the manifest checksum before trusting
	// its contents — the SDK is embedded into every driver built with driverforge.
	sum := sha256.Sum256(raw)
	if err := releaseverify.Verify("Driverforge SDK", hex.EncodeToString(sum[:]), rel.SHA256); err != nil {
		return err
	}

	lua, err := extractBundle(raw)
	if err != nil {
		return fmt.Errorf("sdkrelease: %s: %w", rel.ZipName, err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".driverforge-sdk-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, writeErr := tmp.Write(lua)
	closeErr := tmp.Close()
	if writeErr != nil {
		_ = os.Remove(tmpName)
		return writeErr
	}
	if closeErr != nil {
		_ = os.Remove(tmpName)
		return closeErr
	}
	if err := os.Rename(tmpName, dest); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// extractBundle pulls driverforge-sdk.lua out of the release zip.
func extractBundle(zipData []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("not a valid zip: %w", err)
	}
	for _, f := range zr.File {
		if filepath.Base(f.Name) != BundleName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(io.LimitReader(rc, 8<<20))
	}
	return nil, fmt.Errorf("zip does not contain %s", BundleName)
}
