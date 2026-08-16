package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/deploycache"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

const xmlV1 = `<devicedata>
  <name>Test</name>
  <version>000001</version>
  <config>
    <script jit="1" file="driver.lua"/>
    <actions>
      <action><name>Alpha</name><command>A</command></action>
    </actions>
  </config>
</devicedata>`

// xmlV2 renames the action → structural change.
const xmlV2 = `<devicedata>
  <name>Test</name>
  <version>000002</version>
  <config>
    <script jit="1" file="driver.lua"/>
    <actions>
      <action><name>Renamed</name><command>A</command></action>
    </actions>
  </config>
</devicedata>`

func writeC4z(t *testing.T, path, driverXML, driverLua string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range map[string]string{"driver.xml": driverXML, "driver.lua": driverLua} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// fakeAgent serves the deploy-server protocol over TLS. All shared state is
// mutex-guarded so the test is clean under -race (handlers run on server
// goroutines).
type fakeAgent struct {
	mu       sync.Mutex
	state    map[string]string // filename -> recorded structural hash
	syncBody string            // last driver.lua received by /sync
}

func newFakeAgent() *fakeAgent { return &fakeAgent{state: map[string]string{}} }

func (f *fakeAgent) setBaseline(filename, hash string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state[filename] = hash
}

func (f *fakeAgent) lastSyncBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syncBody
}

func (f *fakeAgent) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"agentVersion":"0.1.0","protocol":2,"capabilities":["deploy","sync","driver-state"]}`)
	})
	mux.HandleFunc("/sync", func(w http.ResponseWriter, r *http.Request) {
		fn, h := r.Header.Get("X-Driver-Filename"), r.Header.Get("X-Driver-Structural-Hash")
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		switch base := f.state[fn]; {
		case base == "":
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"ok":false,"error":"not_installed"}`)
		case base != h:
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"ok":false,"error":"needs_deploy"}`)
		default:
			f.syncBody = string(body)
			io.WriteString(w, `{"ok":true,"synced":1,"devices":["Ctrl"],"structuralHash":"`+h+`","verified":true}`)
		}
	})
	mux.HandleFunc("/driver-state", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		h := f.state[r.URL.Query().Get("driver")]
		f.mu.Unlock()
		installed := "false"
		if h != "" {
			installed = "true"
		}
		io.WriteString(w, `{"ok":true,"installed":`+installed+`,"structuralHash":"`+h+`"}`)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// setupSyncEnv isolates config and cache in *separate* temp dirs (so clearing
// the cache never touches credentials/context), points the agent at srv, and
// records a logged-in user with a selected org + device.
func setupSyncEnv(t *testing.T, srv *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(dir, "cache"))
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	t.Setenv("DRIVERFORGE_AGENT_INSECURE", "1")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	t.Setenv("DRIVERFORGE_AGENT_PORT", u.Port())
	if err := orgctx.SetCurrent(orgctx.Org{ID: "o1", Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	if err := orgctx.SetCurrentDevice(orgctx.Device{ID: "d1", Name: "Ctrl", OrgID: "o1", IP: u.Hostname()}); err != nil {
		t.Fatal(err)
	}
}

// clearDeployCache removes the local baseline cache (only — config is elsewhere).
func clearDeployCache(t *testing.T) {
	t.Helper()
	if cacheDir, err := os.UserCacheDir(); err == nil {
		_ = os.RemoveAll(filepath.Join(cacheDir, "driverforge"))
	}
}

func TestSyncFlow(t *testing.T) {
	f := newFakeAgent()
	setupSyncEnv(t, f.start(t))
	noDeploy := func() error { return errors.New("unexpected deploy fallback") }

	c4z := filepath.Join(t.TempDir(), "test.c4z")
	writeC4z(t, c4z, xmlV1, "print('v1')")
	hv1, _ := driverxml.StructuralHash([]byte(xmlV1))
	ctx := context.Background()

	// Warm cache, unchanged → hot-swap.
	f.setBaseline("test.c4z", hv1)
	_ = deploycache.Set("d1", "test.c4z", hv1)
	var out bytes.Buffer
	if err := runSync(ctx, &out, c4z, "test", "", false, noDeploy); err != nil {
		t.Fatalf("hot-swap sync failed: %v", err)
	}
	if f.lastSyncBody() != "print('v1')" {
		t.Errorf("agent got body %q, want the driver.lua", f.lastSyncBody())
	}
	// Heading, a resolved hot-swap step, and the success block with its context.
	for _, want := range []string{"🚀 Syncing test to Ctrl", "✓ Lua hot-swapped on 1 device", "✓ sync complete", "controller Ctrl"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}

	// Structural change (renamed action) → escalate; non-interactive => error.
	writeC4z(t, c4z, xmlV2, "print('v2')")
	out.Reset()
	if err := runSync(ctx, &out, c4z, "test", "", false, noDeploy); err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("structural change should escalate to deploy, got %v", err)
	}

	// Cold cache but agent knows the baseline → driver-state probe → hot-swap.
	writeC4z(t, c4z, xmlV1, "print('v1b')")
	clearDeployCache(t)
	out.Reset()
	if err := runSync(ctx, &out, c4z, "test", "", false, noDeploy); err != nil {
		t.Fatalf("cold-cache sync failed: %v", err)
	}
	if f.lastSyncBody() != "print('v1b')" {
		t.Errorf("cold-cache agent got body %q", f.lastSyncBody())
	}
}

func TestSyncNoBaselineEscalates(t *testing.T) {
	f := newFakeAgent() // agent knows nothing
	setupSyncEnv(t, f.start(t))
	noDeploy := func() error { return errors.New("unexpected deploy fallback") }

	c4z := filepath.Join(t.TempDir(), "test.c4z")
	writeC4z(t, c4z, xmlV1, "print('x')")

	var out bytes.Buffer
	err := runSync(context.Background(), &out, c4z, "test", "", false, noDeploy)
	if err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("no-baseline should escalate to deploy, got %v", err)
	}
}

// The command-level guarantee: `driverforge sync` builds before shipping,
// so a stale artifact in dist/ can never be pushed — what the agent receives
// always matches the current source.
func TestSyncCommandBuildsBeforeShipping(t *testing.T) {
	f := newFakeAgent()
	setupSyncEnv(t, f.start(t))

	// A real buildable project whose dist/ holds a STALE artifact.
	projDir := t.TempDir()
	srcDir := filepath.Join(projDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(srcDir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("driver.xml", xmlV1)
	write("manifest.c4zproj", `<Driver type="c4z" name="test">
  <Items>
    <Item type="file" name="driver.xml"/>
    <Item type="file" name="driver.lua"/>
  </Items>
</Driver>`)
	write("driver.lua", "print('fresh')")
	writeDriverforgeConfig(t, projDir)
	distDir := filepath.Join(projDir, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeC4z(t, filepath.Join(distDir, "test.c4z"), xmlV1, "print('stale')")

	// Point the CLI at the project, force plain (non-TUI) output.
	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	// First sync: builds (replacing the stale artifact) and escalates because
	// the agent has no baseline — proving the build ran is the next step.
	var out bytes.Buffer
	cmd := newSyncCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected escalation with no baseline")
	}

	// The built artifact establishes the real structural hash; give the agent
	// that baseline and cache it, as a confirmed deploy would have.
	builtXML, err := readC4zEntry(filepath.Join(distDir, "test.c4z"), "driver.xml")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := driverxml.StructuralHash(builtXML)
	if err != nil {
		t.Fatal(err)
	}
	f.setBaseline("test.c4z", hash)
	_ = deploycache.Set("d1", "test.c4z", hash)

	// Edit the source and sync again: the agent must receive the NEW Lua, not
	// the stale artifact's — sync rebuilt before shipping.
	write("driver.lua", "print('fresher')")
	out.Reset()
	cmd = newSyncCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("sync after baseline: %v\n%s", err, out.String())
	}
	if got := f.lastSyncBody(); got != "print('fresher')" {
		t.Errorf("agent received %q, want the freshly built Lua (stale artifact shipped?)", got)
	}
}

// writeDriverforgeConfig marks the project as initialised (ship commands and
// --increment require it).
func writeDriverforgeConfig(t *testing.T, projDir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(projDir, ".driverforge"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"schema":1,"org":"test-org","project":{"slug":"test","name":"Test"},"sdkVersion":"1.0.0","buildTargets":[],"entry":"driver.lua","versioning":"integer"}`
	if err := os.WriteFile(filepath.Join(projDir, ".driverforge", "config.json"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeBuildableProject lays out a minimal driver project without .driverforge/.
func writeBuildableProject(t *testing.T) string {
	t.Helper()
	projDir := t.TempDir()
	srcDir := filepath.Join(projDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"driver.xml": xmlV1,
		"manifest.c4zproj": `<Driver type="c4z" name="test">
  <Items>
    <Item type="file" name="driver.xml"/>
    <Item type="file" name="driver.lua"/>
  </Items>
</Driver>`,
		"driver.lua": "print('x')",
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(srcDir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return projDir
}

// Ship commands manage the driver's version, so they require an initialised
// project; plain build (and build alone) must keep working without one.
func TestShipCommandsRequireInitialisedProject(t *testing.T) {
	f := newFakeAgent()
	setupSyncEnv(t, f.start(t))
	projDir := writeBuildableProject(t) // deliberately no .driverforge/

	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	for _, mk := range []struct {
		name string
		cmd  func() *cobra.Command
	}{
		{"sync", newSyncCmd},
		{"deploy", newDeployCmd},
	} {
		t.Run(mk.name, func(t *testing.T) {
			var out bytes.Buffer
			cmd := mk.cmd()
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{})
			err := cmd.ExecuteContext(context.Background())
			var ue *clierr.UserError
			if !errors.As(err, &ue) {
				t.Fatalf("error = %v, want a clierr.UserError", err)
			}
			if ue.Title != "Not set up for Driverforge" {
				t.Errorf("title = %q, want Not set up for Driverforge", ue.Title)
			}
		})
	}

	// Plain build must still work on the same uninitialised project.
	var out bytes.Buffer
	cmd := newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("plain build on an uninitialised project must work: %v", err)
	}
}

// --increment mutates the version, which needs the versioning decision made
// during init; without it the flag errors with a friendly card.
func TestBuildIncrementRequiresInitialisedProject(t *testing.T) {
	projDir := writeBuildableProject(t) // no .driverforge/

	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	var out bytes.Buffer
	cmd := newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--increment"})
	err := cmd.ExecuteContext(context.Background())
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a clierr.UserError", err)
	}
	if ue.Title != "Versioning not configured" {
		t.Errorf("title = %q, want Versioning not configured", ue.Title)
	}

	// With an initialised project the same invocation succeeds.
	writeDriverforgeConfig(t, projDir)
	out.Reset()
	cmd = newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--increment"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("build --increment on an initialised project: %v\n%s", err, out.String())
	}
	src, _ := os.ReadFile(filepath.Join(projDir, "src", "driver.xml"))
	if !strings.Contains(string(src), "<version>000002</version>") {
		t.Errorf("integer scheme should bump 000001 -> 000002 and persist:\n%s", src)
	}
}

// --version is the driverpackager escape hatch for build scripts: it supplies
// the version explicitly, so it must work with no .driverforge/config.json (unlike
// --increment, which needs the init-time versioning decision).
func TestBuildVersionFlagWorksUninitialised(t *testing.T) {
	projDir := writeBuildableProject(t) // no .driverforge/

	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	outDir := filepath.Join(t.TempDir(), "artifacts")
	var out bytes.Buffer
	cmd := newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version", "2026070501", "-o", outDir})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("build --version on an uninitialised project: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "test.c4z")); err != nil {
		t.Fatalf("artifact not in --output-dir: %v", err)
	}
	src, _ := os.ReadFile(filepath.Join(projDir, "src", "driver.xml"))
	if !strings.Contains(string(src), "<version>2026070501</version>") {
		t.Errorf("source driver.xml not stamped:\n%s", src)
	}

	// --version and --increment are mutually exclusive.
	out.Reset()
	cmd = newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version", "1", "--increment"})
	if err := cmd.ExecuteContext(context.Background()); err == nil {
		t.Fatal("expected mutual-exclusion error for --version + --increment")
	}
}

// A config predating the versioning field can't bump: deploy (which always
// bumps) errors with the re-run-init card rather than guessing a scheme.
func TestDeployRequiresVersioningScheme(t *testing.T) {
	projDir := writeBuildableProject(t)
	// Initialised, but no versioning field (an older config).
	if err := os.MkdirAll(filepath.Join(projDir, ".driverforge"), 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema":1,"org":"o","project":{"slug":"p","name":"P"},"sdkVersion":"1.0.0","buildTargets":[],"entry":"driver.lua"}`
	if err := os.WriteFile(filepath.Join(projDir, ".driverforge", "config.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	var out bytes.Buffer
	cmd := newDeployCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})
	err := cmd.ExecuteContext(context.Background())
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a clierr.UserError", err)
	}
	if ue.Title != "Versioning not configured" {
		t.Errorf("title = %q, want Versioning not configured", ue.Title)
	}
}

// The date scheme stamps today's first slot (or the next), persisted to source.
func TestBuildIncrementDateScheme(t *testing.T) {
	projDir := writeBuildableProject(t)
	writeDriverforgeConfig(t, projDir)
	// Switch the scheme to date.
	cfgPath := filepath.Join(projDir, ".driverforge", "config.json")
	data, _ := os.ReadFile(cfgPath)
	patched := strings.Replace(string(data), `"versioning":"integer"`, `"versioning":"date"`, 1)
	if err := os.WriteFile(cfgPath, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}

	oldProjectDir, oldNoTUI := flagProjectDir, flagNoTUI
	flagProjectDir, flagNoTUI = projDir, true
	t.Cleanup(func() { flagProjectDir, flagNoTUI = oldProjectDir, oldNoTUI })

	var out bytes.Buffer
	cmd := newBuildCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--increment"})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("build --increment with date scheme: %v\n%s", err, out.String())
	}
	want := time.Now().Format("20060102") + "01"
	src, _ := os.ReadFile(filepath.Join(projDir, "src", "driver.xml"))
	if !strings.Contains(string(src), "<version>"+want+"</version>") {
		t.Errorf("date scheme should stamp %s:\n%s", want, src)
	}
}
