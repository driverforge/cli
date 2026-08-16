package initdriver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/project"
)

// fixtureProject writes a minimal plain driver and returns its Project.
func fixtureProject(t *testing.T) project.Project {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.lua", "function OnDriverInit()\n    Log:Info(\"hi\")\nend\n")
	write("manifest.c4zproj", `<Driver type="c4z" name="acme">
  <Items>
    <Item type="file" name="driver.lua"/>
  </Items>
</Driver>
`)
	return project.Project{Dir: dir, SrcDir: src, DistDir: filepath.Join(dir, "dist")}
}

func changeByRel(r *Result, rel string) *Change {
	for i := range r.Changes {
		if filepath.ToSlash(r.Changes[i].RelPath) == rel {
			return &r.Changes[i]
		}
	}
	return nil
}

func TestPlanInlineMode(t *testing.T) {
	proj := fixtureProject(t)
	d := Decision{SDKBundle: []byte("-- sdk\n"), SDKVersion: "1.0.0", ProjectToken: "drv_xyz", LoggerGlobal: "Log"}

	res, err := Plan(proj, d)
	if err != nil {
		t.Fatal(err)
	}

	if c := changeByRel(res, "src/driver.lua"); c == nil {
		t.Error("expected driver.lua change")
	} else if !strings.Contains(string(c.New), `Anvil:Init("drv_xyz"`) {
		t.Errorf("inline key not in entry:\n%s", c.New)
	}
	if c := changeByRel(res, "src/vendor/anvil-sdk.lua"); c == nil || c.Kind != Create {
		t.Error("expected vendor SDK create")
	}
	if c := changeByRel(res, "src/manifest.c4zproj"); c == nil {
		t.Error("expected manifest change")
	}
	// Inline mode creates no build-config files.
	if changeByRel(res, "src/config.lua") != nil || changeByRel(res, "src/config.release.lua") != nil {
		t.Error("inline mode should not scaffold config files")
	}
}

func TestPlanBuildTargetsMode(t *testing.T) {
	proj := fixtureProject(t)
	d := Decision{
		SDKBundle: []byte("-- sdk\n"), ProjectToken: "drv_xyz",
		BuildTargets: true, Targets: []string{"debug", "release"},
	}
	res, err := Plan(proj, d)
	if err != nil {
		t.Fatal(err)
	}
	// config.lua is the committed default (debug); release gets an override.
	for _, rel := range []string{"src/config.lua", "src/config.release.lua"} {
		if changeByRel(res, rel) == nil {
			t.Errorf("expected %s to be scaffolded", rel)
		}
	}
	// Default target has no separate file — config.lua is it.
	if changeByRel(res, "src/config.debug.lua") != nil {
		t.Error("the default target should be config.lua, not config.debug.lua")
	}
	// Config files are safe to commit — no .gitignore and no sample template.
	if changeByRel(res, "src/.gitignore") != nil || changeByRel(res, "src/.env.sample.lua") != nil {
		t.Error("build targets should not scaffold a .gitignore or sample")
	}
	// The Driverforge key is always inlined, even with build targets — it never routes
	// through the CONFIG table.
	c := changeByRel(res, "src/driver.lua")
	if c == nil || !strings.Contains(string(c.New), `Anvil:Init("drv_xyz"`) {
		t.Errorf("build-target entry should still inline the key:\n%v", c)
	}
	if c != nil && strings.Contains(string(c.New), "CONFIG.DRIVERFORGE_API_KEY") {
		t.Errorf("build-target entry should not route the key through the CONFIG table:\n%s", c.New)
	}
	// Config files hold per-build config, not the Driverforge key.
	if f := changeByRel(res, "src/config.lua"); f == nil || strings.Contains(string(f.New), "drv_xyz") {
		t.Error("config.lua must not carry the Driverforge key")
	}
	if f := changeByRel(res, "src/config.release.lua"); f == nil || strings.Contains(string(f.New), "drv_xyz") {
		t.Error("config.release.lua must not carry the Driverforge key")
	}
}

func TestPlanIdempotentAfterApply(t *testing.T) {
	proj := fixtureProject(t)
	d := Decision{
		SDKBundle: []byte("-- sdk\n"), ProjectToken: "drv_xyz", LoggerGlobal: "Log",
		BuildTargets: true, Targets: []string{"debug", "release"},
	}

	res, err := Plan(proj, d)
	if err != nil {
		t.Fatal(err)
	}
	// Apply the plan to disk.
	for _, c := range res.Changes {
		if err := os.MkdirAll(filepath.Dir(c.AbsPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(c.AbsPath, c.New, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Re-planning should now be a no-op.
	again, err := Plan(proj, d)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Empty() {
		var rels []string
		for _, c := range again.Changes {
			rels = append(rels, c.RelPath)
		}
		t.Errorf("re-plan should be empty, got changes: %v", rels)
	}
}

// A squishy Main that escapes the driver must be rejected (init would otherwise
// patch a file outside src/).
func TestEntryFileRejectsEscapingMain(t *testing.T) {
	proj := fixtureProject(t)
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "squishy"),
		[]byte(`Main "../../../etc/evil.lua"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := EntryFile(proj.SrcDir)
	if err == nil {
		t.Fatal("an escaping squishy Main must be rejected")
	}
	if !strings.Contains(err.Error(), "outside the driver") {
		t.Errorf("unexpected error: %v", err)
	}
}

// A local squishy Main is accepted and returned as the entry.
func TestEntryFileAcceptsLocalMain(t *testing.T) {
	proj := fixtureProject(t)
	if err := os.WriteFile(filepath.Join(proj.SrcDir, "squishy"),
		[]byte(`Main "src/main.lua"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := EntryFile(proj.SrcDir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.ToSlash(got) != "src/main.lua" {
		t.Errorf("entry = %q, want src/main.lua", got)
	}
}

// A permission-denied read must not be planned as a Create (which would drop
// Old from the diff and let rollback delete the real file).
func TestAddFileSurfacesNonNotExistError(t *testing.T) {
	proj := fixtureProject(t)
	// A directory where a file is expected: ReadFile returns a non-ENOENT error.
	clash := filepath.Join(proj.SrcDir, "config.lua")
	if err := os.MkdirAll(clash, 0o755); err != nil {
		t.Fatal(err)
	}
	r := &Result{}
	if err := r.addFile(proj, clash, []byte("x"), false); err == nil {
		t.Fatal("a non-ENOENT read error must surface, not be planned as Create")
	}
	if len(r.Changes) != 0 {
		t.Errorf("no change should be planned on a read error, got %d", len(r.Changes))
	}
}
