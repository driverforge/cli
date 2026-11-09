package initdriver

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/pkg/c4z/manifest"
)

// Check is one thing `driverforge init` sets up, and whether it is still in
// place. The set mirrors Plan's steps — Plan is the repair, these are the
// questions Plan answers by emitting (or not emitting) a change.
type Check struct {
	Name string // short label, e.g. "entry wiring"
	What string // what it should be, for the report
	OK   bool
}

// Diagnosis is the result of inspecting a project's Driverforge integration.
type Diagnosis struct {
	Checks []Check
	// EntryPatchNeeded is true when the driver's entry Lua is missing the SDK
	// wiring. Repairing that needs the project's drv_ token, which is not in
	// .driverforge/config.json — so it is the one repair that costs a graph
	// lookup, and callers use this to avoid paying for it otherwise.
	EntryPatchNeeded bool
}

// Healthy reports whether every check passed.
func (d Diagnosis) Healthy() bool {
	for _, c := range d.Checks {
		if !c.OK {
			return false
		}
	}
	return true
}

// Failed returns just the checks that did not pass, for reporting.
func (d Diagnosis) Failed() []Check {
	var out []Check
	for _, c := range d.Checks {
		if !c.OK {
			out = append(out, c)
		}
	}
	return out
}

// Diagnose inspects what init set up, reading files only — no network, and no
// project token, so it is cheap enough for `driverforge upgrade` to call on
// every run. It answers "is this project still wired up", not "is it current":
// an out-of-date SDK is a healthy project, and is upgrade's business.
//
// A check that cannot be evaluated (an unreadable entry, an unparseable
// manifest) counts as FAILED rather than passing: reporting a project healthy
// on the strength of a file we could not read is the mistake this whole path
// exists to stop making.
func Diagnose(proj project.Project, buildTargets bool, dirs ...string) Diagnosis {
	src := proj.SrcDir
	sdkPath := sdkDir(dirs...)
	var d Diagnosis

	// 1. Entry Lua carries the require + Driverforge:Init.
	entryOK := false
	if entryRel, err := EntryFile(src); err == nil {
		if entrySrc, rerr := os.ReadFile(filepath.Join(src, entryRel)); rerr == nil {
			entryOK = alreadyPatched(entrySrc)
		}
	}
	d.EntryPatchNeeded = !entryOK
	d.Checks = append(d.Checks, Check{
		Name: "entry wiring",
		What: "the driver entry requires the SDK and calls Driverforge:Init",
		OK:   entryOK,
	})

	// 2. The vendored bundle is present and non-empty. An empty file is not a
	// driver that works.
	st, serr := os.Stat(VendorBundlePath(src, sdkPath))
	d.Checks = append(d.Checks, Check{
		Name: "embedded SDK",
		What: filepath.ToSlash(filepath.Join(sdkPath, filepath.Base(VendorBundlePath(src, sdkPath)))) + " is present",
		OK:   serr == nil && st.Size() > 0,
	})

	// 3. Every selected manifest squishes Lua and ships the SDK directory.
	manifestPaths := proj.ManifestPaths
	if len(manifestPaths) == 0 {
		manifestPaths = []string{proj.ManifestPath}
	}
	if manifestPaths[0] == "" {
		manifestPaths[0] = filepath.Join(src, "manifest.c4zproj")
	}
	squishOK, vendorOK := true, true
	for _, manifestPath := range manifestPaths {
		manSrc, err := os.ReadFile(manifestPath)
		if err != nil {
			squishOK, vendorOK = false, false
			break
		}
		doc, err := manifest.ParseBytes(manSrc, filepath.Base(manifestPath))
		if err != nil {
			squishOK, vendorOK = false, false
			break
		}
		squishOK = squishOK && doc.SquishLua
		vendorOK = vendorOK && hasSDKItem(doc, sdkPath)
	}
	d.Checks = append(d.Checks,
		Check{Name: "squishLua", What: `the manifest sets squishLua="true"`, OK: squishOK},
		Check{Name: "SDK directory item", What: filepath.ToSlash(sdkPath) + " is shipped by the manifest", OK: vendorOK},
	)

	// 4. The squishy declares every module the patched entry requires. The
	// squisher's preload carries only declared modules, so a missing one is a
	// driver-load LUA_ERROR on the controller — not a cosmetic omission.
	sdkDeclared, configDeclared := false, !buildTargets
	if sqSrc, err := os.ReadFile(filepath.Join(src, "squishy")); err == nil {
		sq := string(sqSrc)
		sdkDeclared = strings.Contains(sq, `"`+sdkRelPath(sdkPath)+`"`)
		if buildTargets {
			configDeclared = strings.Contains(sq, `"config.lua"`)
		}
	}
	d.Checks = append(d.Checks, Check{
		Name: "squishy SDK module",
		What: "squishy declares the vendored SDK module",
		OK:   sdkDeclared,
	})
	if buildTargets {
		d.Checks = append(d.Checks, Check{
			Name: "squishy config module",
			What: "squishy declares the config module",
			OK:   configDeclared,
		})
	}
	return d
}
