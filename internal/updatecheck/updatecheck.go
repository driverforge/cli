// Package updatecheck implements a polite "a newer version is available" notice
// (an update *notifier*, not a self-updater).
//
// It reads the same release manifest the Driverforge SDK and agent publish to
// Azure Blob Storage (https://releases.driverforge.com/driverforge-releases/<artifact>/
// latest/manifest.json — a JSON doc whose `version` field is the latest release),
// compares it to the embedded build version, and prints a one-line, channel-aware
// upgrade hint to stderr.
//
// It is designed to be unobtrusive: at most one network request per day (results
// are cached), never on stdout, never in CI/non-TTY/scripts, and easily opted out
// of via DRIVERFORGE_NO_UPDATE_CHECK (or the standard NO_UPDATE_NOTIFIER).
package updatecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	defaultManifestURL = "https://releases.driverforge.com/driverforge-releases/driverforge-cli/latest/manifest.json"
	checkInterval      = 24 * time.Hour
)

var httpClient = &http.Client{Timeout: 3 * time.Second}

// Notify prints an upgrade hint to w if a newer release is available. It is a
// no-op when checks are disabled, when running headless (CI / non-TTY), or when
// the current build is not a released version (e.g. a local "dev" build). Any
// network or cache error is swallowed — an update check must never disrupt a
// command.
func Notify(ctx context.Context, current string, w io.Writer) {
	if os.Getenv("DRIVERFORGE_NO_UPDATE_CHECK") != "" || os.Getenv("NO_UPDATE_NOTIFIER") != "" || os.Getenv("CI") != "" {
		return
	}
	if _, ok := parseVersion(current); !ok {
		return // dev / non-release build — nothing meaningful to compare
	}
	if f, ok := w.(*os.File); ok && !term.IsTerminal(int(f.Fd())) {
		return // piped/redirected — don't pollute non-interactive output
	}

	latest := cachedOrFetchLatest(ctx, manifestURL())
	if latest == "" {
		return
	}
	if cmp, ok := compareVersions(latest, current); !ok || cmp <= 0 {
		return
	}
	fmt.Fprintln(w, formatNotice(current, latest))
}

// LatestCLI returns the latest released driverforge CLI version, or "" when
// unknown. It always fetches the manifest live, bypassing the daily cache —
// `driverforge upgrade` is an explicit ask, and a day-old answer would hide a
// release the user already knows about. A successful fetch refreshes the cache
// so the passive notifier benefits; a failure leaves the cache alone and
// returns "" (the caller renders "couldn't check").
func LatestCLI(ctx context.Context) string {
	latest, err := latestFromManifest(ctx, manifestURL())
	if err != nil {
		return ""
	}
	writeCache(cacheData{LatestVersion: latest, CheckedAt: time.Now()})
	return latest
}

// ManifestURL is the CLI release manifest location. Exported so the self-updater
// resolves the same store this package checks against — an upgrade must install
// the release the notice announced, not one from a different origin.
func ManifestURL() string { return manifestURL() }

// manifestURL is the CLI release manifest location (env-overridable for tests).
func manifestURL() string {
	if u := os.Getenv("DRIVERFORGE_UPDATE_MANIFEST_URL"); u != "" {
		return u
	}
	return defaultManifestURL
}

// IsNewer reports whether candidate is a strictly newer release than current.
// False if either is not a parseable release version.
func IsNewer(candidate, current string) bool {
	cmp, ok := compareVersions(candidate, current)
	return ok && cmp > 0
}

// UpgradeHint returns the right CLI-upgrade command for how this binary was
// installed (brew/scoop/installer), for display by `driverforge upgrade`.
func UpgradeHint() string { return upgradeHint() }

type manifest struct {
	Version string `json:"version"`
}

type cacheData struct {
	LatestVersion string    `json:"latestVersion"`
	CheckedAt     time.Time `json:"checkedAt"`
}

// cachedOrFetchLatest returns the latest version, using the cache when it is
// fresh and otherwise refreshing it. On a fetch error it records the failed
// attempt (with any stale version) so the TTL is honoured — otherwise a
// firewalled or blackholed network would re-hit the manifest, and pay the
// client's timeout, on every single command.
func cachedOrFetchLatest(ctx context.Context, url string) string {
	if c, ok := readCache(); ok && time.Since(c.CheckedAt) < checkInterval {
		return c.LatestVersion
	}
	latest, err := latestFromManifest(ctx, url)
	if err != nil {
		stale := ""
		if c, ok := readCache(); ok {
			stale = c.LatestVersion // keep the last known good so we can still notify
		}
		writeCache(cacheData{LatestVersion: stale, CheckedAt: time.Now()})
		return stale
	}
	writeCache(cacheData{LatestVersion: latest, CheckedAt: time.Now()})
	return latest
}

func latestFromManifest(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("update manifest: HTTP %d", resp.StatusCode)
	}
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&m); err != nil {
		return "", err
	}
	if m.Version == "" {
		return "", fmt.Errorf("update manifest: missing version")
	}
	return m.Version, nil
}

func formatNotice(current, latest string) string {
	return fmt.Sprintf("\ndriverforge %s is available (you have %s). Upgrade: %s", latest, current, upgradeHint())
}

// upgradeHint returns the right upgrade command for how this binary was
// installed, inferred from the executable's path.
//
// Published contract: the go.driverforge.com slugs below are printed by every
// released binary that outlives this one — an old CLI's only route to an upgrade
// is the URL it already has baked in. Repoint the slug in Shlink when a target
// moves; never retire it. /get in particular is piped straight into a shell, so
// it must always resolve to the install script and nothing else.
func upgradeHint() string {
	exe, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
			exe = resolved
		}
	}
	return hintForPath(exe, runtime.GOOS)
}

// hintForPath is upgradeHint's decision, split out so the mapping from install
// location to command can be tested without being the binary under test.
func hintForPath(exe, goos string) string {
	// Normalise both separators rather than filepath.ToSlash, which only rewrites
	// backslashes on Windows builds — this decision should not depend on the host
	// that compiled it.
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	switch {
	case strings.Contains(p, "/cellar/"), strings.Contains(p, "/caskroom/"),
		strings.Contains(p, "/homebrew/"), strings.Contains(p, "linuxbrew"):
		// The refresh is not optional: `brew upgrade` compares against the cask
		// definition in the local tap checkout, so without `brew update` first it
		// reports everything up to date and exits 0. Telling a user to run a
		// command that silently does nothing is worse than telling them nothing.
		return "brew update && brew upgrade driverforge"
	case strings.Contains(p, "/scoop/"):
		// Same staleness trap: bare `scoop update` is what refreshes the bucket
		// manifests that `scoop update <app>` then compares against.
		return "scoop update && scoop update driverforge"
	case goos == "windows":
		// The POSIX shell installer can't run on Windows; point at the docs.
		return "see https://go.driverforge.com/install"
	default:
		return `sh -c "$(curl -fsSL https://go.driverforge.com/get)"`
	}
}

// parseVersion parses a "MAJOR.MINOR.PATCH" version (any leading 'v' and any
// pre-release/build suffix are ignored). ok is false for non-release strings.
func parseVersion(v string) ([3]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// compareVersions returns 1 if a>b, -1 if a<b, 0 if equal. ok is false unless
// both values share a comparable scheme: semver "MAJOR.MINOR.PATCH" (the CLI
// and SDK), or a plain date-stamped integer (the agent's YYYYMMDDNN builds).
// Mixed schemes are not comparable.
func compareVersions(a, b string) (int, bool) {
	if av, aok := parseVersion(a); aok {
		bv, bok := parseVersion(b)
		if !bok {
			return 0, false
		}
		for i := 0; i < 3; i++ {
			if av[i] != bv[i] {
				if av[i] > bv[i] {
					return 1, true
				}
				return -1, true
			}
		}
		return 0, true
	}
	ai, aok := parseIntVersion(a)
	bi, bok := parseIntVersion(b)
	if !aok || !bok {
		return 0, false
	}
	switch {
	case ai > bi:
		return 1, true
	case ai < bi:
		return -1, true
	}
	return 0, true
}

// parseIntVersion parses a date-stamped integer version (the agent's scheme,
// e.g. "2026072201"). ok is false for anything else.
func parseIntVersion(v string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func cacheFile() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "driverforge", "update-check.json")
}

func readCache() (cacheData, bool) {
	path := cacheFile()
	if path == "" {
		return cacheData{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheData{}, false
	}
	var c cacheData
	if err := json.Unmarshal(data, &c); err != nil {
		return cacheData{}, false
	}
	return c, true
}

func writeCache(c cacheData) {
	path := cacheFile()
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if data, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
}
