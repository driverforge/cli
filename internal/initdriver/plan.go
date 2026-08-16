package initdriver

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/pkg/c4z/squish"
)

// ChangeKind distinguishes a brand-new file from an edit to an existing one.
type ChangeKind int

const (
	Create ChangeKind = iota
	Modify
)

// Change is a single planned file write. Old is nil for a Create. The command
// layer renders Old→New as a diff and only writes after approval.
type Change struct {
	RelPath string // path relative to the project root, for display
	AbsPath string
	Kind    ChangeKind
	Old     []byte
	New     []byte
}

// Result is the full set of planned changes plus any advisory warnings.
type Result struct {
	Changes  []Change
	Warnings []string
}

// Empty reports whether there's nothing to do (already fully initialised).
func (r *Result) Empty() bool { return len(r.Changes) == 0 }

// Plan computes the changes that retrofit the located driver for Driverforge, reading
// the current files but writing nothing.
func Plan(proj project.Project, d Decision) (*Result, error) {
	res := &Result{}
	src := proj.SrcDir

	// 1. Entry Lua: wrap OnDriverInit.
	entryRel, err := EntryFile(src)
	if err != nil {
		return nil, err
	}
	entryAbs := filepath.Join(src, entryRel)
	entrySrc, err := os.ReadFile(entryAbs)
	if err != nil {
		return nil, &clierr.UserError{
			Title:   "Driver entry not found",
			Message: fmt.Sprintf("couldn't read the driver entry %s", entryRel),
			Hint:    "run driverforge init from a driver project with a src/driver.lua",
			Err:     err,
		}
	}
	patched, warn := PatchEntry(entrySrc, d)
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	res.addModify(proj, entryAbs, entrySrc, patched)

	// 2. Embed the SDK bundle.
	vendorAbs := filepath.Join(src, "vendor", "driverforge-sdk.lua")
	if err := res.addFile(proj, vendorAbs, d.SDKBundle, true); err != nil {
		return nil, err
	}

	// 3. Manifest: vendor item + squishLua.
	manifestAbs := filepath.Join(src, "manifest.c4zproj")
	if manSrc, err := os.ReadFile(manifestAbs); err == nil {
		newMan, err := PatchManifest(manSrc)
		if err != nil {
			return nil, err
		}
		res.addModify(proj, manifestAbs, manSrc, newMan)

		// 3b. Squishy: the manifest patch forces squishLua, so the squishy
		// must exist and declare every module the patched entry requires —
		// the squisher's preload carries only declared modules, and a missing
		// one is a driver-load LUA_ERROR on the controller.
		squishyAbs := filepath.Join(src, "squishy")
		if sqSrc, sqErr := os.ReadFile(squishyAbs); sqErr == nil {
			res.addModify(proj, squishyAbs, sqSrc, PatchSquishy(sqSrc, d.BuildTargets))
		} else if errors.Is(sqErr, fs.ErrNotExist) {
			if err := res.addFile(proj, squishyAbs, NewSquishy(entryRel, d.BuildTargets), false); err != nil {
				return nil, err
			}
		} else {
			return nil, fmt.Errorf("reading squishy: %w", sqErr)
		}
	} else {
		return nil, &clierr.UserError{
			Title:   "Not an Driverforge driver project",
			Message: "src/manifest.c4zproj is missing",
			Hint:    "run driverforge init from a driver project",
			Err:     err,
		}
	}

	// 4. Build configurations (opt-in): config.lua is the committed default (the
	// first target); each additional target gets a config.<target>.lua override the
	// build swaps in for `driverforge build --configuration <target>`. All committed &
	// real (no secrets — the Driverforge key is inlined in the driver), so no
	// gitignore/sample.
	if d.BuildTargets && len(d.Targets) > 0 {
		def := d.Targets[0]
		// Never clobber an existing config the author may have edited.
		if err := res.addFile(proj, filepath.Join(src, "config.lua"), configContent(def, true), false); err != nil {
			return nil, err
		}
		for _, t := range d.Targets[1:] {
			if err := res.addFile(proj, filepath.Join(src, "config."+t+".lua"), configContent(t, false), false); err != nil {
				return nil, err
			}
		}
	}

	return res, nil
}

// EntryFile returns the driver's entry Lua path relative to src: the squishy
// Main when a squishy is present, else driver.lua.
func EntryFile(src string) (string, error) {
	squishyPath := filepath.Join(src, "squishy")
	if _, err := os.Stat(squishyPath); err == nil {
		sq, err := squish.ParseFile(squishyPath)
		if err != nil {
			return "", fmt.Errorf("parsing squishy: %w", err)
		}
		if m := strings.TrimSpace(sq.Main); m != "" {
			rel := filepath.FromSlash(m)
			// The squishy Main is embedded in the manifest and drives where init
			// patches the entry file — reject a path that escapes the driver
			// (e.g. ../../../Users/x/.zshrc) so init can't write outside src/.
			if !filepath.IsLocal(rel) {
				return "", &clierr.UserError{
					Title:   "Invalid squishy Main",
					Message: fmt.Sprintf("the squishy Main path %q points outside the driver", m),
					Hint:    "set Main to a path inside src/ in the squishy file",
				}
			}
			return rel, nil
		}
	}
	return "driver.lua", nil
}

// addModify records an edit to an existing file, skipping it when unchanged.
func (r *Result) addModify(proj project.Project, abs string, old, new []byte) {
	if bytes.Equal(old, new) {
		return
	}
	r.Changes = append(r.Changes, Change{
		RelPath: relTo(proj, abs),
		AbsPath: abs,
		Kind:    Modify,
		Old:     old,
		New:     new,
	})
}

// addFile records a new (or, when overwrite is set, possibly updated) file. When
// overwrite is false an existing file is left untouched. A read error other than
// "not exist" (e.g. permission denied) is returned rather than mistaken for an
// absent file — planning a Create over a real file would drop its Old from the
// diff and rollback would os.Remove it.
func (r *Result) addFile(proj project.Project, abs string, content []byte, overwrite bool) error {
	old, err := os.ReadFile(abs)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		r.Changes = append(r.Changes, Change{
			RelPath: relTo(proj, abs), AbsPath: abs, Kind: Create, New: content,
		})
	case err != nil:
		return fmt.Errorf("reading %s: %w", relTo(proj, abs), err)
	case overwrite && !bytes.Equal(old, content):
		r.Changes = append(r.Changes, Change{
			RelPath: relTo(proj, abs), AbsPath: abs, Kind: Modify, Old: old, New: content,
		})
	}
	return nil
}

func relTo(proj project.Project, abs string) string {
	if rel, err := filepath.Rel(proj.Dir, abs); err == nil {
		return rel
	}
	return abs
}

// configContent renders config.lua (isDefault) or a config.<target>.lua override.
// These hold per-build configuration — service URLs, endpoints, flags — NOT
// secrets (the Driverforge key is inlined in driver.lua), so they're committed and real
// (require('config') resolves; IDEs/linters are happy).
func configContent(target string, isDefault bool) []byte {
	if isDefault {
		return []byte(fmt.Sprintf(`-- Driverforge config (default), used by 'driverforge build' and via require('config').
-- Put per-build config here — service URLs, endpoints, flags. Safe to commit;
-- keep secrets out of it. Override for other builds in config.<target>.lua.

CONFIG = {}
CONFIG.C4_CONFIGURATION = %q

-- Example: CONFIG.VENDOR_OAUTH_URL = "https://dev.vendor.example/oauth"
`, target))
	}
	return []byte(fmt.Sprintf(`-- Driverforge config override for the %s build.
-- 'driverforge build --configuration %s' swaps this in as config.lua. Put this build's
-- values here — service URLs, endpoints, flags. Safe to commit; keep secrets out.

CONFIG = {}
CONFIG.C4_CONFIGURATION = %q

-- Example: CONFIG.VENDOR_OAUTH_URL = "https://vendor.example/oauth"
`, target, target, target))
}
