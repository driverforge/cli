package cli

import (
	"path/filepath"
	"testing"

	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/sdkrelease"
)

// The planner writes the bundle exactly where the release cache names it. If
// these ever disagree, a healthy project reports as broken (or the reverse).
// Bundle-presence behaviour itself is covered by initdriver's Diagnose tests.
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
