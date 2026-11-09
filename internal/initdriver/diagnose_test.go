package initdriver

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/driverforge/cli/internal/project"
)

// healthyProject writes a tree in the exact shape init leaves behind.
func healthyProject(t *testing.T) project.Project {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(filepath.Join(src, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(src, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.lua", "function OnDriverInit()\n    require('vendor.driverforge-sdk')\n    Driverforge:Init(\"drv_x\")\nend\n")
	write(filepath.Join("vendor", "driverforge-sdk.lua"), "-- sdk\n")
	write("manifest.c4zproj", `<Driver type="c4z" name="acme" squishLua="true">
  <Items>
    <Item type="file" name="driver.lua"/>
    `+vendorItem+`
  </Items>
</Driver>
`)
	write("squishy", "Main \"driver.lua\"\n\n"+squishySDKModule()+"\n")
	return project.Project{Dir: dir, SrcDir: src}
}

func failedNames(d Diagnosis) map[string]bool {
	out := map[string]bool{}
	for _, c := range d.Failed() {
		out[c.Name] = true
	}
	return out
}

func TestDiagnoseHealthy(t *testing.T) {
	d := Diagnose(healthyProject(t), false)
	if !d.Healthy() {
		t.Errorf("healthy project reported unhealthy: %+v", d.Failed())
	}
	if d.EntryPatchNeeded {
		t.Error("EntryPatchNeeded on a wired entry — would cost a needless graph lookup")
	}
}

// The reported scenario: SDK downloaded into place, but the entry never wired.
func TestDiagnoseUnwiredEntry(t *testing.T) {
	proj := healthyProject(t)
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "driver.lua"),
		[]byte("function OnDriverInit()\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := Diagnose(proj, false)
	if d.Healthy() {
		t.Fatal("an unwired entry must not report healthy")
	}
	if !failedNames(d)["entry wiring"] {
		t.Errorf("entry wiring not flagged: %+v", d.Failed())
	}
	if !d.EntryPatchNeeded {
		t.Error("EntryPatchNeeded should be set — repairing this needs the drv_ token")
	}
}

// A commented-out call is not wiring; alreadyPatched strips comments, and
// Diagnose must inherit that rather than matching raw text.
func TestDiagnoseIgnoresCommentedWiring(t *testing.T) {
	proj := healthyProject(t)
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "driver.lua"),
		[]byte("-- Driverforge:Init(\"drv_x\")\nfunction OnDriverInit()\nend\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !failedNames(Diagnose(proj, false))["entry wiring"] {
		t.Error("a commented-out Init must not count as wired")
	}
}

func TestDiagnoseMissingBundle(t *testing.T) {
	proj := healthyProject(t)
	if err := os.Remove(VendorBundlePath(proj.SrcDir)); err != nil {
		t.Fatal(err)
	}
	if !failedNames(Diagnose(proj, false))["embedded SDK"] {
		t.Error("a missing bundle must be flagged")
	}
}

func TestDiagnoseEmptyBundleCountsAsMissing(t *testing.T) {
	proj := healthyProject(t)
	if err := os.WriteFile(VendorBundlePath(proj.SrcDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !failedNames(Diagnose(proj, false))["embedded SDK"] {
		t.Error("a truncated bundle is not a driver that works")
	}
}

func TestDiagnoseManifestAndSquishy(t *testing.T) {
	proj := healthyProject(t)
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "manifest.c4zproj"),
		[]byte("<Driver type=\"c4z\" name=\"acme\">\n  <Items>\n    <Item type=\"file\" name=\"driver.lua\"/>\n  </Items>\n</Driver>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "squishy"), []byte("Main \"driver.lua\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := failedNames(Diagnose(proj, false))
	for _, want := range []string{"squishLua", "SDK directory item", "squishy SDK module"} {
		if !got[want] {
			t.Errorf("%q not flagged", want)
		}
	}
}

// A file we cannot read must count as failed. Reporting healthy on the strength
// of an unreadable file is the exact mistake this path exists to prevent.
func TestDiagnoseUnreadableFilesFail(t *testing.T) {
	dir := t.TempDir()
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	d := Diagnose(proj, false)
	if d.Healthy() {
		t.Error("an empty/absent source tree must not report healthy")
	}
	if len(d.Failed()) != len(d.Checks) {
		t.Errorf("expected every check to fail, got %d of %d", len(d.Failed()), len(d.Checks))
	}
}

// With build targets on, the config module is an extra requirement.
func TestDiagnoseBuildTargetsRequireConfigModule(t *testing.T) {
	proj := healthyProject(t)
	if failedNames(Diagnose(proj, true))["squishy config module"] == false {
		t.Error("build targets should require the config module declaration")
	}
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "squishy"),
		[]byte("Main \"driver.lua\"\n"+squishySDKModule()+"\n"+squishyConfigModule+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !Diagnose(proj, true).Healthy() {
		t.Errorf("both modules declared should be healthy: %+v", Diagnose(proj, true).Failed())
	}
}
