package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/versioning"
)

// handleAlreadyInit handles a project that already carries .driverforge/config.json:
// it reports the setup and stops. Init sets a project up exactly once — it never
// mutates an existing one; SDK updates go through the `driverforge upgrade` picker.
func handleAlreadyInit(out io.Writer, proj project.Project, cfg *driverforgeconfig.Config) error {
	// Configs written before the versioning field existed: run the interview
	// once and record the choice, so --increment and deploys can bump.
	if !versioning.Valid(cfg.Versioning) {
		scheme, err := gatherVersioningScheme(out, proj)
		if err != nil {
			return err
		}
		cfg.Versioning = scheme
		data, err := driverforgeconfig.Marshal(*cfg)
		if err != nil {
			return err
		}
		if err := os.WriteFile(driverforgeconfig.Path(proj.Dir), data, 0o644); err != nil {
			return err
		}
	}
	rows := [][2]string{
		{"project", cfg.Project.Name},
		{"SDK", cfg.SDKVersion},
	}
	if len(cfg.BuildTargets) > 0 {
		rows = append(rows, [2]string{"targets", strings.Join(cfg.BuildTargets, ", ")})
	}
	tip := "Run `driverforge upgrade` to update the embedded SDK."
	if ui.Interactive(flagNoTUI) {
		ui.Fprintln(out, "\n"+ui.WarningBlock("This project is already set up for Driverforge.", rows, tip))
		return nil
	}
	fmt.Fprintln(out, "\nThis project is already set up for Driverforge.")
	fmt.Fprintf(out, "\n  Project:  %s\n  SDK:      %s\n", cfg.Project.Name, cfg.SDKVersion)
	if len(cfg.BuildTargets) > 0 {
		fmt.Fprintf(out, "  Targets:  %s\n", strings.Join(cfg.BuildTargets, ", "))
	}
	fmt.Fprintln(out, "\nRun `driverforge upgrade` to update the embedded SDK.")
	return nil
}

// runSDKUpdate re-embeds the Anvil SDK at the version resolved from constraint
// (empty/"latest" → the newest published; an exact x.y.z → that build) and bumps
// .driverforge/config.json. It is the engine behind the `driverforge upgrade`
// picker's SDK row — selecting the row there is the consent, so it prompts for
// nothing (a dirty git tree is still refused up front).
func runSDKUpdate(ctx context.Context, out io.Writer, proj project.Project, cfg *driverforgeconfig.Config, constraint string) error {
	if err := requireCleanTree(ctx, proj.Dir); err != nil {
		return err
	}

	target, err := resolveSDKTarget(ctx, out, constraint)
	if err != nil {
		return err
	}
	// No early return on target == cfg.SDKVersion: even when the config records
	// the current version, the vendor bundle may be missing or hand-mangled, and
	// re-embedding it is a repair. fetchSDK is a cache hit for an already-held
	// version, and the len(changes) == 0 check below is the real "nothing to do".

	bundle, err := fetchSDK(ctx, out, target)
	if err != nil {
		return err
	}

	newCfg := *cfg
	newCfg.SDKVersion = target
	cfgBytes, err := driverforgeconfig.Marshal(newCfg)
	if err != nil {
		return err
	}

	var changes []initdriver.Change
	vendorCh, ok, err := fileChange(proj.Dir, filepath.Join(proj.SrcDir, "vendor", "anvil-sdk.lua"), bundle)
	if err != nil {
		return err
	}
	if ok {
		changes = append(changes, vendorCh)
	}
	cfgCh, ok, err := fileChange(proj.Dir, driverforgeconfig.Path(proj.Dir), cfgBytes)
	if err != nil {
		return err
	}
	if ok {
		changes = append(changes, cfgCh)
	}
	if len(changes) == 0 {
		fmt.Fprintf(out, "\n%s\n", ui.Success(fmt.Sprintf("Already on Anvil SDK %s — nothing to update.", target)))
		return nil
	}

	if err := executeFileChanges(ctx, out, proj, "", changes); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s\n", ui.Celebrate(fmt.Sprintf("Updated to Anvil SDK %s!", target)))
	return nil
}

// resolveSDKTarget turns a constraint into the concrete version to install. Empty
// or "latest" resolves the newest published build (a network lookup behind a
// spinner); an exact x.y.z is taken as-is; a partial/invalid constraint is a
// friendly error (sdkrelease.ResolveConstraint).
func resolveSDKTarget(ctx context.Context, out io.Writer, constraint string) (string, error) {
	c := strings.TrimSpace(constraint)
	if c != "" && c != "latest" {
		if _, err := sdkrelease.ResolveConstraint(c); err != nil {
			return "", err
		}
		return c, nil
	}
	var version string
	err := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), "Checking for the latest Anvil SDK…", func() (string, error) {
		rel, e := sdkrelease.Resolve(ctx, "latest")
		if e != nil {
			return "", e
		}
		version = rel.Version
		return "Latest Anvil SDK: " + rel.Version, nil
	})
	if err != nil {
		return "", err
	}
	return version, nil
}

// fetchSDK returns the bundle for an exact version, cache-first. Unlike init's
// resolveSDK it never asks permission to download — the caller already chose to
// update — it just shows a spinner while fetching an uncached build.
func fetchSDK(ctx context.Context, out io.Writer, version string) ([]byte, error) {
	if sdkrelease.IsCached(version) {
		p, err := sdkrelease.CachedPath(version)
		if err != nil {
			return nil, err
		}
		bundle, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(out, "\n%s\n", ui.Success("Using installed Anvil SDK "+version))
		return bundle, nil
	}
	p, _, err := downloadSDK(ctx, out, version, fmt.Sprintf("Downloading Anvil SDK %s…", version))
	if err != nil {
		return nil, err
	}
	return os.ReadFile(p)
}

// loadSDK resolves the SDK bundle to embed and reads it, printing the version.
// It is latest-first: one manifest round-trip resolves the current release, a
// cached copy of that version is used without downloading, and only a NEW
// version asks to download. When the release store is unreachable it falls
// back to the newest cached bundle (with a visible warning), so init still
// works offline. Updating later is the `driverforge upgrade` picker.
func loadSDK(ctx context.Context, out io.Writer) (bundle []byte, version string, err error) {
	bundlePath, version, cached, err := resolveSDK(ctx, out)
	if err != nil {
		return nil, "", err
	}
	bundle, err = os.ReadFile(bundlePath)
	if err != nil {
		return nil, "", err
	}
	// A fresh download already announced itself via the spinner's ✓ line; only
	// the cached case needs a line here.
	if cached {
		fmt.Fprintf(out, "\n%s\n", ui.Success(fmt.Sprintf("Using installed Anvil SDK %s (latest)", version)))
	}
	return bundle, version, nil
}

// sdkResolveTimeout bounds the latest-release manifest check so an offline
// init falls back to the cache quickly instead of hanging on the store.
const sdkResolveTimeout = 5 * time.Second

// resolveSDK resolves the bundle to embed, latest-first: the store's current
// release (cache-satisfied when already downloaded), falling back to the
// newest cached bundle when the store can't be reached. Embedding the newest
// CACHED version unconditionally — the previous policy — silently produced
// stale inits once the cache lagged the store.
func resolveSDK(ctx context.Context, out io.Writer) (path, version string, cached bool, err error) {
	rctx, cancel := context.WithTimeout(ctx, sdkResolveTimeout)
	defer cancel()
	rel, rerr := sdkrelease.Resolve(rctx, "latest")
	if rerr != nil {
		// Store unreachable — offline init still works from the cache, but say
		// so: the cached bundle may lag the actual latest.
		if v, p, ok, cerr := sdkrelease.NewestCached(); cerr == nil && ok {
			fmt.Fprintf(out, "\n%s\n", ui.Warning(fmt.Sprintf("couldn't reach the release store — using installed Anvil SDK %s (run `driverforge upgrade` later to check for updates)", v)))
			return p, v, false, nil // false: the warning already announced it
		}
		return "", "", false, clierr.WrapT(rerr, "Anvil SDK unavailable",
			"couldn't reach the release store and no Anvil SDK is installed locally",
			"check your connection and re-run `driverforge init`")
	}

	if sdkrelease.IsCached(rel.Version) {
		p, err := sdkrelease.CachedPath(rel.Version)
		if err != nil {
			return "", "", false, err
		}
		return p, rel.Version, true, nil
	}
	ok, err := ui.Confirm(
		fmt.Sprintf("Anvil SDK %s isn't installed locally. Download it now?", rel.Version),
		"Fetched from releases.driverforge.com into your local cache.", true)
	if err != nil {
		return "", "", false, err
	}
	if !ok {
		return "", "", false, sdkDownloadDeclined()
	}
	p, v, err := downloadSDK(ctx, out, rel.Version, fmt.Sprintf("Downloading Anvil SDK %s…", rel.Version))
	return p, v, false, err
}

// downloadSDK fetches the SDK for constraint behind a spinner, replacing it with
// a ✓ line naming the resolved version.
func downloadSDK(ctx context.Context, out io.Writer, constraint, label string) (path, version string, err error) {
	werr := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), label, func() (string, error) {
		p, v, e := sdkrelease.EnsureCached(ctx, constraint)
		if e != nil {
			return "", e
		}
		path, version = p, v
		return fmt.Sprintf("Downloaded Anvil SDK %s", v), nil
	})
	if werr != nil {
		return "", "", werr
	}
	return path, version, nil
}

// sdkDownloadDeclined is the friendly stop when the user declines the download —
// init can't proceed without an SDK to embed.
func sdkDownloadDeclined() error {
	return &clierr.UserError{
		Title:   "Anvil SDK required",
		Message: "driverforge init needs the Anvil SDK to continue",
		Hint:    "re-run and allow the download",
	}
}
