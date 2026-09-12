package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/releaseverify"
	"github.com/driverforge/cli/internal/ui"
)

// gitRepo creates a repo with one committed file and returns its path. The
// identity is passed per command so the test never depends on the machine's
// git config.
func gitRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "driver.lua"), []byte("-- driver\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// dirty adds an uncommitted file — the user's in-progress work that an SDK
// upgrade must not be mixed into.
func dirty(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "wip.lua"), []byte("-- wip\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pendingSDKRow is the row sdkRow has built by the time it knows an update
// exists: setup intact, current version known, state not yet decided.
func pendingSDKRow(dir string) (compRow, project.Project, *driverforgeconfig.Config) {
	r := compRow{name: "Driverforge SDK", current: "0.3.0", latest: "—"}
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	return r, proj, &driverforgeconfig.Config{SDKVersion: "0.3.0"}
}

// A dirty project still shows the installed and available versions, but the
// row is blocked rather than selectable, and it says what would unblock it.
func TestSDKUpdateRowBlockedOnDirtyTree(t *testing.T) {
	dir := gitRepo(t)
	dirty(t, dir)
	r, proj, cfg := pendingSDKRow(dir)

	got := sdkUpdateRow(context.Background(), io.Discard, r, proj, cfg, "0.5.0")

	if got.state != stateBlocked {
		t.Fatalf("state = %v, want stateBlocked", got.state)
	}
	if got.current != "0.3.0" || got.latest != "0.5.0" {
		t.Errorf("versions = %s → %s, want 0.3.0 → 0.5.0 shown even when blocked", got.current, got.latest)
	}
	if !strings.Contains(got.note, sdkDirtyTreeNote) {
		t.Errorf("note = %q, want it to carry %q", got.note, sdkDirtyTreeNote)
	}
	if got.run != nil {
		t.Error("a blocked row must not carry a run — the picker can't select it, and nothing else may invoke it")
	}
	if !strings.Contains(got.notesURL, "tag/v0.5.0") {
		t.Errorf("notesURL = %q, want the release notes kept for the blocked row", got.notesURL)
	}
	// The blocked row maps to the picker's blocked state, which is dimmed and
	// unselectable — and the working-tree change it protects is untouched.
	if rows := buildUpgradeRows([]compRow{got}); rows[0].State != ui.UpgradeBlocked {
		t.Errorf("picker state = %v, want UpgradeBlocked", rows[0].State)
	}
	if _, err := os.Stat(filepath.Join(dir, "wip.lua")); err != nil {
		t.Errorf("the user's uncommitted file was disturbed: %v", err)
	}
}

// A clean project gets the selectable row with its run wired up.
func TestSDKUpdateRowSelectableOnCleanTree(t *testing.T) {
	dir := gitRepo(t)
	r, proj, cfg := pendingSDKRow(dir)

	got := sdkUpdateRow(context.Background(), io.Discard, r, proj, cfg, "0.5.0")

	if got.state != stateUpdate {
		t.Fatalf("state = %v, want stateUpdate", got.state)
	}
	if got.run == nil {
		t.Error("a selectable SDK row must carry its run")
	}
	if got.cmd != "driverforge upgrade" {
		t.Errorf("cmd = %q", got.cmd)
	}
	if got.latest != "0.5.0" {
		t.Errorf("latest = %q", got.latest)
	}
}

// Outside git there is no tree to keep clean, so the row is selectable.
func TestSDKUpdateRowSelectableOutsideGit(t *testing.T) {
	r, proj, cfg := pendingSDKRow(t.TempDir())
	got := sdkUpdateRow(context.Background(), io.Discard, r, proj, cfg, "0.5.0")
	if got.state != stateUpdate || got.run == nil {
		t.Fatalf("state = %v, run nil = %v; want a selectable row", got.state, got.run == nil)
	}
}

// The execution-time backstop names the operation that refused, so a tree
// that became dirty after the picker opened produces an SDK-upgrade error,
// not one about driverforge init.
func TestRequireCleanTreeNamesTheOperation(t *testing.T) {
	dir := gitRepo(t)
	dirty(t, dir)
	ctx := context.Background()

	err := requireCleanTree(ctx, dir, "The SDK upgrade")
	if err == nil {
		t.Fatal("dirty tree should be refused")
	}
	if !clierr.IsUser(err) {
		t.Fatalf("want a UserError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "The SDK upgrade") || strings.Contains(err.Error(), "driverforge init") {
		t.Errorf("error = %q, want it to name the SDK upgrade and not init", err.Error())
	}

	if err := requireCleanTree(ctx, dir, "driverforge init"); err == nil || !strings.Contains(err.Error(), "driverforge init") {
		t.Errorf("init's error = %v, want it to name driverforge init", err)
	}
	if err := requireCleanTree(ctx, t.TempDir(), "The SDK upgrade"); err != nil {
		t.Errorf("outside git: %v, want nil", err)
	}
}

// initialisedProject lays out a driver in the exact shape init leaves behind
// (wired entry, vendored bundle, patched manifest and squishy, config.json
// recording sdkVersion) inside a git repo with everything committed, so
// sdkRow's diagnosis passes and only the version comparison and the tree
// state decide the row.
func initialisedProject(t *testing.T, sdkVersion string) string {
	t.Helper()
	dir := gitRepo(t)
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
    <Item type="dir" c4zDir="vendor" name="vendor" recurse="true" exclude="false"/>
  </Items>
</Driver>
`)
	write("squishy", "Main \"driver.lua\"\n\nModule \"vendor.driverforge-sdk\" \"vendor/driverforge-sdk.lua\"\n")

	cfg, err := driverforgeconfig.Marshal(driverforgeconfig.Config{SDKVersion: sdkVersion})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(driverforgeconfig.Path(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driverforgeconfig.Path(dir), cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// fakeSDKStore publishes a signed "latest" SDK manifest at version.
func fakeSDKStore(t *testing.T, version string) {
	t.Helper()
	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	t.Cleanup(restore)
	var manifest []byte
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.json.sig"):
			w.Write(sign(manifest))
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	manifest = []byte(fmt.Sprintf(`{"version":%q,"artifacts":[{"name":"driverforge-sdk-%s.zip","url":%q}]}`,
		version, version, srv.URL+"/driverforge-sdk/"+version+"/driverforge-sdk.zip"))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
}

// The full row: an initialised project behind the store's latest is offered
// the upgrade when its tree is clean, and shown-but-blocked when it isn't.
func TestSDKRowBlocksDirtyProjectEndToEnd(t *testing.T) {
	fakeSDKStore(t, "0.5.0")
	dir := initialisedProject(t, "0.3.0")
	old := flagProjectDir
	flagProjectDir = dir
	t.Cleanup(func() { flagProjectDir = old })

	if r := sdkRow(context.Background(), io.Discard); r.state != stateUpdate || r.run == nil {
		t.Fatalf("clean project: state = %v, run nil = %v, note = %q; want a selectable update", r.state, r.run == nil, r.note)
	}

	dirty(t, dir)
	r := sdkRow(context.Background(), io.Discard)
	if r.state != stateBlocked {
		t.Fatalf("dirty project: state = %v, note = %q; want stateBlocked", r.state, r.note)
	}
	if r.current != "0.3.0" || r.latest != "0.5.0" {
		t.Errorf("versions = %s → %s, want 0.3.0 → 0.5.0", r.current, r.latest)
	}
	if !strings.Contains(r.note, sdkDirtyTreeNote) {
		t.Errorf("note = %q", r.note)
	}
}

// The execution-time backstop: a tree that turned dirty after the picker
// offered the row makes runSDKUpdate refuse before touching anything, with
// an error about the SDK upgrade.
func TestRunSDKUpdateRefusesDirtyTree(t *testing.T) {
	dir := initialisedProject(t, "0.3.0")
	dirty(t, dir)
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	cfg := &driverforgeconfig.Config{SDKVersion: "0.3.0"}

	err := runSDKUpdate(context.Background(), io.Discard, proj, cfg, "0.5.0")
	if err == nil || !clierr.IsUser(err) {
		t.Fatalf("err = %v, want a UserError", err)
	}
	if !strings.Contains(err.Error(), "The SDK upgrade") || strings.Contains(err.Error(), "driverforge init") {
		t.Errorf("error = %q, want it to name the SDK upgrade", err.Error())
	}
	got, err := driverforgeconfig.Load(dir)
	if err != nil || got.SDKVersion != "0.3.0" {
		t.Errorf("config after refusal = %+v, %v; want untouched at 0.3.0", got, err)
	}
}
