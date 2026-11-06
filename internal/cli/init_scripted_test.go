package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/releaseverify"
)

// initGraph answers the graph queries init makes and counts project creations.
type initGraph struct {
	created atomic.Int32
	name    atomic.Value
}

func (g *initGraph) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		reply := func(data any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		}
		project := map[string]any{"id": "p1", "name": "Pool Pump", "slug": "pool-pump", "platform": "control4", "organizationId": "o1", "token": "tok-123"}
		switch q := req.Query; {
		case strings.Contains(q, "myOrganizations"):
			reply(map[string]any{"myOrganizations": []any{map[string]any{"id": "o1", "name": "Acme", "slug": "acme", "myRole": "OWNER"}}})
		case strings.Contains(q, "myProjects"):
			reply(map[string]any{"myProjects": []any{project}})
		case strings.Contains(q, "platforms"):
			reply(map[string]any{"platforms": []any{
				map[string]any{"id": "control4", "name": "Control4", "supported": true},
				map[string]any{"id": "crestron", "name": "Crestron", "comingSoon": true},
			}})
		case strings.Contains(q, "createProject"):
			g.created.Add(1)
			in := req.Variables["input"].(map[string]any)
			g.name.Store(in["name"])
			p := map[string]any{"id": "p2", "name": in["name"], "slug": "new-pump", "platform": in["platform"], "organizationId": "o1", "token": "tok-new"}
			reply(map[string]any{"createProject": p})
		default:
			t.Errorf("unexpected graph query: %s", q)
		}
	}
}

// initEnv signs in, serves the graph and a release store whose latest SDK
// (0.6.0) is already cached, and turns structured output on.
func initEnv(t *testing.T) *initGraph {
	t.Helper()
	isolateSDKCache(t, "0.6.0")
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	t.Setenv("DRIVERFORGE_NO_METRICS", "1")
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	t.Cleanup(restore)
	manifest := []byte(`{"version":"0.6.0","artifacts":[{"name":"driverforge-sdk-0.6.0.zip","url":"https://unused.example/x.zip","sha256":"00"}]}`)
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sig") {
			w.Write(sign(manifest))
			return
		}
		w.Write(manifest)
	}))
	t.Cleanup(store.Close)
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", store.URL)

	g := &initGraph{}
	srv := httptest.NewServer(g.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)
	return g
}

// plainDriver writes a driver init hasn't touched, with a Log logger.
func plainDriver(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range map[string]string{
		"src/driver.xml":       `<devicedata><name>demo</name><version>000001</version><config><script jit="1" file="driver.lua"/></config></devicedata>`,
		"src/manifest.c4zproj": `<Driver type="c4z" name="demo-driver" squishLua="false"><Items><Item type="file" name="driver.lua"/><Item type="file" name="driver.xml"/></Items></Driver>`,
		"src/driver.lua":       "function OnDriverInit()\n  Log:Info(\"hello\")\nend\n",
	} {
		p := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func gitCommitAll(t *testing.T, dir string) {
	t.Helper()
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_COMMITTER_NAME"} {
		t.Setenv(k, "Test")
	}
	for _, k := range []string{"GIT_AUTHOR_EMAIL", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(k, "test@example.test")
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// Without a terminal, one failure names every unanswered question and what
// init detected, before any network call.
func TestInitWithoutTerminalNamesEveryMissingFlag(t *testing.T) {
	initEnv(t)
	t.Setenv("DRIVERFORGE_GRAPH_URL", "http://127.0.0.1:1") // nothing may be asked of the graph
	dir := plainDriver(t)
	gitCommitAll(t, dir)

	stdout, code := runFailingIn(t, dir, "init")
	if code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	e := decode(t, stdout)["error"].(map[string]any)
	if e["code"] != codeInputRequired {
		t.Fatalf("code = %v", e["code"])
	}
	details := e["details"].(map[string]any)
	var missing []string
	for _, m := range details["missing"].([]any) {
		missing = append(missing, m.(string))
	}
	want := []string{"--project or --create-project", "--org", "--versioning", "--build-configs or --no-build-configs",
		"--logger or --no-logger", "--branch or --no-branch", "--dry-run or --yes"}
	if strings.Join(missing, "|") != strings.Join(want, "|") {
		t.Errorf("missing = %q\nwant      %q", missing, want)
	}
	detected := details["detected"].(map[string]any)
	if detected["logger"] != "Log" || detected["versioning"] == "" {
		t.Errorf("detected = %v", detected)
	}
}

// A dry run reports the plan and diffs, creates no project and writes nothing.
func TestInitDryRunChangesNothing(t *testing.T) {
	g := initEnv(t)
	dir := plainDriver(t)
	before, _ := os.ReadFile(filepath.Join(dir, "src", "driver.lua"))

	stdout, _, code := run(t, dir, "init", "--org", "acme", "--create-project", "New Pump", "--versioning", "date",
		"--no-build-configs", "--logger", "Log", "--dry-run")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	doc := decode(t, stdout)
	if doc["applied"] != false || doc["dry_run"] != true {
		t.Errorf("applied/dry_run = %v/%v", doc["applied"], doc["dry_run"])
	}
	p := doc["project"].(map[string]any)
	if p["action"] != "create" || p["name"] != "New Pump" || p["platform"] != "control4" {
		t.Errorf("project = %v", p)
	}
	var entryDiff string
	for _, c := range doc["changes"].([]any) {
		c := c.(map[string]any)
		if c["path"] == "src/driver.lua" {
			entryDiff, _ = c["diff"].(string)
		}
		if d, _ := c["diff"].(string); strings.Contains(d, "\x1b[") {
			t.Errorf("%s diff carries terminal colour", c["path"])
		}
	}
	if !strings.Contains(entryDiff, "+ ") {
		t.Errorf("no diff for the entry file:\n%s", stdout)
	}
	if g.created.Load() != 0 {
		t.Error("a dry run created a project")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "src", "driver.lua"))
	if string(after) != string(before) {
		t.Error("a dry run wrote the entry file")
	}
	if _, err := os.Stat(filepath.Join(dir, ".driverforge", "config.json")); !os.IsNotExist(err) {
		t.Error("a dry run wrote .driverforge/config.json")
	}
}

// --yes creates the project, writes its token into the driver and reports it.
func TestInitYesCreatesTheProject(t *testing.T) {
	g := initEnv(t)
	dir := plainDriver(t)

	stdout, _, code := run(t, dir, "init", "--org", "acme", "--create-project", "New Pump", "--platform", "Control4",
		"--description", "pumps", "--versioning", "integer", "--build-configs", "--no-logger", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if g.created.Load() != 1 || g.name.Load() != "New Pump" {
		t.Fatalf("created %d project(s), name %v", g.created.Load(), g.name.Load())
	}
	doc := decode(t, stdout)
	p := doc["project"].(map[string]any)
	if doc["applied"] != true || p["action"] != "create" || p["id"] != "p2" || p["slug"] != "new-pump" {
		t.Errorf("doc = %v", doc)
	}
	if doc["build_configs"] != true || doc["versioning"] != "integer" || doc["logger"] != "" {
		t.Errorf("shape = %v", doc)
	}
	entry, _ := os.ReadFile(filepath.Join(dir, "src", "driver.lua"))
	if !strings.Contains(string(entry), "tok-new") {
		t.Errorf("the created project's token isn't in the entry file:\n%s", entry)
	}
	cfg, _ := os.ReadFile(filepath.Join(dir, ".driverforge", "config.json"))
	if !strings.Contains(string(cfg), `"new-pump"`) {
		t.Errorf("config.json doesn't name the project:\n%s", cfg)
	}
}

// Linking an existing project onto a new branch commits the change there.
func TestInitYesLinksOnABranch(t *testing.T) {
	g := initEnv(t)
	dir := plainDriver(t)
	gitCommitAll(t, dir)

	stdout, _, code := run(t, dir, "init", "--project", "acme/pool-pump", "--versioning", "date",
		"--no-build-configs", "--logger", "Log", "--branch", "driverforge-init", "--yes")
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, stdout)
	}
	if g.created.Load() != 0 {
		t.Error("linking created a project")
	}
	doc := decode(t, stdout)
	if p := doc["project"].(map[string]any); p["action"] != "link" || p["slug"] != "pool-pump" {
		t.Errorf("project = %v", p)
	}
	if b := git(t, dir, "branch", "--show-current"); b != "driverforge-init" {
		t.Errorf("branch = %q", b)
	}
	if msg := git(t, dir, "log", "-1", "--format=%s"); msg != defaultInitCommitMessage {
		t.Errorf("commit = %q", msg)
	}
	if st := git(t, dir, "status", "--porcelain"); st != "" {
		t.Errorf("left uncommitted:\n%s", st)
	}

	// Run again: already set up, reported as such.
	stdout, _, code = run(t, dir, "init")
	if code != 0 {
		t.Fatalf("second run exit = %d\n%s", code, stdout)
	}
	if doc := decode(t, stdout); doc["already_set_up"] != true || doc["applied"] != false {
		t.Errorf("second run = %v", doc)
	}
}

func TestInitFlagConflicts(t *testing.T) {
	initEnv(t)
	dir := plainDriver(t)
	for _, args := range [][]string{
		{"--project", "acme/x", "--create-project", "X"},
		{"--platform", "control4"},
		{"--org", "acme", "--project", "acme/x"},
		{"--org", "acme"},
		{"--versioning", "semver"},
		{"--build-configs", "--no-build-configs"},
		{"--logger", "Log", "--no-logger"},
		{"--logger", "Log }); os.exit(("},
		{"--branch", "b", "--no-branch"},
		{"--commit-message", "m"},
		{"--yes", "--dry-run"},
	} {
		stdout, code := runFailingIn(t, dir, append([]string{"init"}, args...)...)
		if code != exitUsage || decode(t, stdout)["error"].(map[string]any)["code"] != codeUsage {
			t.Errorf("%v: exit %d, %s", args, code, stdout)
		}
	}
}

func TestChoosePlatform(t *testing.T) {
	ps := []driverforgeapi.Platform{
		{ID: "control4", Name: "Control4", Supported: true},
		{ID: "crestron", Name: "Crestron", ComingSoon: true},
		{ID: "internal", Name: "Internal", Supported: true, Hidden: true},
	}
	if p, err := choosePlatform(ps, ""); err != nil || p.ID != "control4" {
		t.Errorf("sole supported platform: %v, %v", p, err)
	}
	if p, err := choosePlatform(ps, "control4"); err != nil || p.ID != "control4" {
		t.Errorf("by name: %v, %v", p, err)
	}
	for _, flag := range []string{"crestron", "nope", "internal"} {
		if _, err := choosePlatform(ps, flag); err == nil {
			t.Errorf("--platform %s should fail", flag)
		}
	}
	two := append(ps, driverforgeapi.Platform{ID: "savant", Name: "Savant", Supported: true})
	if _, err := choosePlatform(two, ""); err == nil || describe(err, commandFailure).Code != codeInputRequired {
		t.Errorf("two supported platforms without --platform: %v", err)
	}
}

// runFailingIn runs a command in dir that must fail, returning stdout and the
// exit status as structured output would report them.
func runFailingIn(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	return runFailing(t, append([]string{"--project-dir", dir}, args...)...)
}
