package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
)

// newProjectWithSrc returns a Project rooted at a temp dir with src/ created.
func newProjectWithSrc(t *testing.T) project.Project {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	return project.Project{Dir: dir, SrcDir: src}
}

func writeBundle(t *testing.T, proj project.Project, body string) {
	t.Helper()
	p := initdriver.VendorBundlePath(proj.SrcDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The config recording a version is not evidence the bundle is in the tree —
// this is the case that used to report "up to date" about a file nothing opened.
func TestSDKBundleEmbeddedAbsent(t *testing.T) {
	proj := newProjectWithSrc(t)
	if sdkBundleEmbedded(proj) {
		t.Error("no vendor dir at all should not count as embedded")
	}
}

func TestSDKBundleEmbeddedPresent(t *testing.T) {
	proj := newProjectWithSrc(t)
	writeBundle(t, proj, "-- sdk\n")
	if !sdkBundleEmbedded(proj) {
		t.Error("a written bundle should count as embedded")
	}
}

// A truncated bundle is not a driver that works, so zero bytes reads as absent
// rather than as a satisfied install.
func TestSDKBundleEmbeddedRejectsEmptyFile(t *testing.T) {
	proj := newProjectWithSrc(t)
	writeBundle(t, proj, "")
	if sdkBundleEmbedded(proj) {
		t.Error("an empty bundle should not count as embedded")
	}
}

// The pre-rename layout: a vendor dir holding only the old anvil-sdk.lua is not
// an embedded SDK, however current the config claims to be.
func TestSDKBundleEmbeddedIgnoresOtherVendorFiles(t *testing.T) {
	proj := newProjectWithSrc(t)
	vendor := filepath.Join(proj.SrcDir, "vendor")
	if err := os.MkdirAll(vendor, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vendor, "anvil-sdk.lua"), []byte("-- old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if sdkBundleEmbedded(proj) {
		t.Error("only a stale anvil-sdk.lua present — must not count as embedded")
	}
}

// The planner writes the bundle exactly where upgrade looks for it. If these
// ever disagree, upgrade reports a healthy project as broken (or the reverse).
func TestVendorBundlePathMatchesReleaseBundleName(t *testing.T) {
	got := initdriver.VendorBundlePath("/tmp/proj/src")
	want := filepath.Join("/tmp/proj/src", "vendor", sdkrelease.BundleName)
	if got != want {
		t.Errorf("VendorBundlePath = %q, want %q", got, want)
	}
	if filepath.Base(got) != sdkrelease.BundleName {
		t.Errorf("bundle filename = %q, want %q", filepath.Base(got), sdkrelease.BundleName)
	}
}
