package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/deployrecord"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/telemetryread"
)

// fakeGraph answers the telemetry readers' graph operations and records the
// variables each telemetry query was sent.
type fakeGraph struct {
	mu      sync.Mutex
	logs    []map[string]any // what the logs query returns
	logByID map[string]map[string]any
	fail    string // when set, telemetry queries fail with this GraphQL error
	vars    []map[string]any
}

func (g *fakeGraph) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		g.mu.Lock()
		defer g.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		reply := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
		q := req.Query
		switch {
		case strings.Contains(q, "myOrganizations"):
			reply(map[string]any{"myOrganizations": []map[string]any{{"id": "org-1", "name": "Acme", "slug": "acme"}}})
		case strings.Contains(q, "myProjects"):
			reply(map[string]any{"myProjects": []map[string]any{{"id": "proj-1", "name": "Smart TV", "slug": "smart-tv", "organizationId": "org-1"}}})
		case strings.Contains(q, "deployableDevices"):
			reply(map[string]any{"deployableDevices": []map[string]any{
				{"id": "dev-1", "friendlyName": "bench-a", "deviceId": "000FFF9BB74C", "status": "ACTIVE"},
				{"id": "dev-2", "friendlyName": "bench-b", "deviceId": "000FFF000002", "status": "ACTIVE"},
			}})
		case g.fail != "":
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": g.fail}}})
		case strings.Contains(q, "logs("):
			g.vars = append(g.vars, req.Variables)
			reply(map[string]any{"logs": map[string]any{"logs": g.logs, "hasMore": false, "totalCount": len(g.logs), "windowMinutes": 1440}})
		case strings.Contains(q, "log("):
			reply(map[string]any{"log": g.logByID[req.Variables["id"].(string)]})
		case strings.Contains(q, "errorOccurrence("):
			reply(map[string]any{"errorOccurrence": nil})
		default:
			t.Errorf("unexpected graph query: %s", q)
		}
	}
}

// telemetryEnv isolates config and auth, points the CLI at g, and returns an
// empty directory to run from (not a driver repo).
func telemetryEnv(t *testing.T, g *fakeGraph) string {
	t.Helper()
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	t.Setenv("DRIVERFORGE_NO_METRICS", "1")
	t.Setenv("DRIVERFORGE_AGENT_MODE", "false")
	for _, k := range []string{envOrg, envProject, envDevice} {
		t.Setenv(k, "")
	}
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(g.handler(t))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_GRAPH_URL", srv.URL)
	return t.TempDir()
}

// driverRepo makes a linked driver project and returns its root.
func driverRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for path, body := range map[string]string{
		"src/manifest.c4zproj":     "<Driver/>",
		".driverforge/config.json": `{"schema":1,"org":"acme","project":{"slug":"smart-tv","name":"Smart TV"}}`,
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

// run executes the CLI and returns stdout, stderr and the process exit status.
func run(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--no-update-check", "--project-dir", dir}, args...))
	_, err := root.ExecuteC()
	return stdout.String(), stderr.String(), clierr.ExitCode(err)
}

func logRow(id string, ms int64, msg, version string) map[string]any {
	return map[string]any{"id": id, "createdAt": ms, "level": "INFO", "message": msg,
		"traceId": "t-" + id, "driver": "smart_tv.c4z", "driverVersion": version, "controller": "000FFF9BB74C"}
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, s)
	}
	return m
}

func TestLogsVersionCurrentReadsTheRecordedDeploy(t *testing.T) {
	g := &fakeGraph{logs: []map[string]any{logRow("l2", 2000, "second", "12"), logRow("l1", 1000, "first", "12")}}
	telemetryEnv(t, g)
	repo := driverRepo(t)
	if err := orgctx.SetCurrent(orgctx.Org{ID: "org-1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := orgctx.SetCurrentDevice(orgctx.Device{ID: "dev-1", Name: "bench-a", OrgID: "org-1", DeviceID: "000FFF9BB74C"}); err != nil {
		t.Fatal(err)
	}
	if err := deployrecord.Save(deployrecord.Record{Project: "smart-tv", Driver: "smart_tv.c4z",
		Device: deployrecord.Device{ID: "dev-1"}, Version: "12", DeployedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	stdout, _, code := run(t, repo, "logs", "--version", "current", "-o", "json")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stdout)
	}
	out := decode(t, stdout)
	scope := out["scope"].(map[string]any)
	if scope["project"] != "smart-tv" || scope["version"] != "12" || scope["device"].(map[string]any)["name"] != "bench-a" {
		t.Errorf("scope = %v", scope)
	}
	entries := out["entries"].([]any)
	if len(entries) != 2 || entries[0].(map[string]any)["id"] != "l1" {
		t.Errorf("entries = %v, want l1 then l2 (oldest first)", entries)
	}
	filters := g.vars[0]["filters"].(map[string]any)
	if fmt.Sprint(filters["controllers"]) != "[000FFF9BB74C]" || fmt.Sprint(filters["driverVersions"]) != "[12]" {
		t.Errorf("server filters = %v, want the recorded device and version", filters)
	}
}

func TestLogsFlagsAloneWorkOutsideARepo(t *testing.T) {
	g := &fakeGraph{logs: []map[string]any{logRow("l1", 1000, "first", "12")}}
	dir := telemetryEnv(t, g)
	stdout, _, code := run(t, dir, "logs", "--org", "acme", "--project", "smart-tv", "--device", "bench-b", "--version", "12")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stdout)
	}
	if scope := decode(t, stdout)["scope"].(map[string]any); scope["device"].(map[string]any)["id"] != "000FFF000002" {
		t.Errorf("scope = %v, want bench-b", scope)
	}
}

func TestLogsWithoutAProjectExits5WithoutPrompting(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	stdout, _, code := run(t, dir, "logs")
	if code != exitContext {
		t.Fatalf("exit %d, want %d", code, exitContext)
	}
	e := decode(t, stdout)["error"].(map[string]any)
	if e["code"] != codeContextUnresolved || !strings.Contains(e["hint"].(string), "--project") {
		t.Errorf("error = %v", e)
	}
}

func TestLogsWithSeveralDevicesAndNoneSelectedIsAmbiguous(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	stdout, _, code := run(t, dir, "logs", "--project", "smart-tv")
	if code != exitContext {
		t.Fatalf("exit %d, want %d", code, exitContext)
	}
	if e := decode(t, stdout)["error"].(map[string]any); e["code"] != codeContextAmbiguous {
		t.Errorf("error = %v", e)
	}
}

func TestVersionCurrentWithoutARecordExits5(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	stdout, _, code := run(t, dir, "logs", "--project", "smart-tv", "--device", "bench-a", "--version", "current")
	if code != exitContext {
		t.Fatalf("exit %d, want %d", code, exitContext)
	}
	if e := decode(t, stdout)["error"].(map[string]any); e["code"] != codeNoDeployRecorded {
		t.Errorf("error = %v", e)
	}
}

func TestEmptyVersionScopedReadWarnsNoEntries(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	stdout, _, code := run(t, dir, "logs", "--project", "smart-tv", "--device", "bench-a", "--version", "12")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	out := decode(t, stdout)
	warnings := out["warnings"].([]any)
	if out["count"].(float64) != 0 || len(warnings) != 1 || warnings[0].(map[string]any)["code"] != warnNoEntries {
		t.Errorf("out = %v", out)
	}
	if !strings.Contains(warnings[0].(map[string]any)["message"].(string), "--until-match") {
		t.Errorf("no_entries warning lacks the re-run hint: %v", warnings[0])
	}
}

func TestTruncatedPayloadNamesTheGetCommandThatFetchesItWhole(t *testing.T) {
	id := "6f1c2f9e-6c1a-4c39-9a51-0d4b8e1f2a3b"
	big := `{"data":"` + strings.Repeat("x", 200) + `"}`
	row := logRow(id, 1000, big, "12")
	g := &fakeGraph{logs: []map[string]any{row}, logByID: map[string]map[string]any{id: row}}
	dir := telemetryEnv(t, g)

	stdout, _, _ := run(t, dir, "logs", "--project", "smart-tv", "--device", "bench-a", "--max-payload-bytes", "50")
	out := decode(t, stdout)
	entry := out["entries"].([]any)[0].(map[string]any)
	if entry["truncated"] == nil || len(entry["message"].(string)) != 50 {
		t.Fatalf("entry = %v, want the message cut to 50 bytes and marked", entry)
	}
	w := out["warnings"].([]any)[0].(map[string]any)
	cmd := w["details"].(map[string]any)["entries"].([]any)[0].(map[string]any)["command"].(string)
	if w["code"] != warnPayloadTruncated || !strings.HasPrefix(cmd, "driverforge get "+id) {
		t.Fatalf("warning = %v", w)
	}

	args := strings.Fields(strings.TrimPrefix(cmd, "driverforge "))
	stdout, _, code := run(t, dir, append(args, "-o", "json")...)
	if code != 0 {
		t.Fatalf("get exit %d\n%s", code, stdout)
	}
	full := decode(t, stdout)["entry"].(map[string]any)
	if full["message"] != big || full["truncated"] != nil {
		t.Errorf("get returned %v, want the whole message", full)
	}
}

func TestOutputKeysStayWithinTheAllowList(t *testing.T) {
	g := &fakeGraph{logs: []map[string]any{logRow("l1", 1000, "first", "12")}}
	dir := telemetryEnv(t, g)
	stdout, _, _ := run(t, dir, "logs", "--project", "smart-tv", "--device", "bench-a")
	out := decode(t, stdout)
	envelopeKeys := []string{"scope", "entries", "count", "has_more", "next_cursor", "warnings", "wait"}
	for k := range out {
		if !slices.Contains(envelopeKeys, k) {
			t.Errorf("envelope key %q is outside the contract", k)
		}
	}
	for _, e := range out["entries"].([]any) {
		for k := range e.(map[string]any) {
			if !slices.Contains(telemetryread.AllowedKeys, k) {
				t.Errorf("entry key %q is outside the allow-list", k)
			}
		}
	}
}

func TestServerErrorsNeverReachTheOutput(t *testing.T) {
	internal := `Invalid prisma.telemetryLog.findMany() invocation on host db-primary.internal: relation "TelemetryLog" does not exist`
	dir := telemetryEnv(t, &fakeGraph{fail: internal})
	t.Setenv("DRIVERFORGE_AGENT_MODE", "true")
	stdout, stderr, code := run(t, dir, "logs", "--project", "smart-tv", "--device", "bench-a")
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	for _, s := range []string{stdout, stderr} {
		for _, leak := range []string{"prisma", "TelemetryLog", "db-primary", "graph api"} {
			if strings.Contains(s, leak) {
				t.Errorf("output leaks %q:\n%s", leak, s)
			}
		}
	}
	if e := decode(t, stdout)["error"].(map[string]any); e["code"] != codeInternal {
		t.Errorf("error = %v, want internal_error", e)
	}
}

func TestAgentModeStderrIsJSONLines(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	t.Setenv("DRIVERFORGE_AGENT_MODE", "true")
	// A usage error, a context error and a successful jsonl read all keep
	// stderr to JSON lines.
	for _, args := range [][]string{
		{"logs", "--tail", "0"},
		{"logs"},
		{"logs", "--project", "smart-tv", "--device", "bench-a", "-o", "jsonl"},
	} {
		_, stderr, _ := run(t, dir, args...)
		for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
			if line == "" {
				continue
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(line), &v); err != nil {
				t.Errorf("%v: stderr line isn't JSON: %q", args, line)
			}
		}
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	dir := telemetryEnv(t, &fakeGraph{})
	for _, args := range [][]string{
		{"logs", "--tail", "0"},
		{"logs", "--grep", "("},
		{"logs", "--since", "yesterday"},
		{"logs", "--timeout", "11m", "--until-match", "x"},
		{"events", "--field", "level=warn"},
		{"errors", "-o", "yaml"},
		{"logs", "--bogus"},
	} {
		if _, _, code := run(t, dir, args...); code != exitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, exitUsage)
		}
	}
}

func TestValidateDefaults(t *testing.T) {
	t.Setenv("DRIVERFORGE_AGENT_MODE", "false")
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	o, err := readFlags{tail: 5000, untilMatch: "x"}.validate(telemetryread.KindLog, io.Discard, now)
	if err != nil {
		t.Fatal(err)
	}
	if o.tail != telemetryread.MaxPageSize || len(o.warnings) != 1 || o.warnings[0].Code != warnTailClamped {
		t.Errorf("tail %d warnings %v, want clamped to 1000 with a warning", o.tail, o.warnings)
	}
	if o.timeout != defaultWaitTimeout || o.format != formatJSON || o.window != maxLookbackMinutes {
		t.Errorf("timeout %s format %s window %d", o.timeout, o.format, o.window)
	}

	o, _ = readFlags{tail: 1, follow: true, since: "10m"}.validate(telemetryread.KindLog, io.Discard, now)
	if o.timeout != 0 || o.format != formatJSONL || o.window != 11 || !o.filter.Since.Equal(now.Add(-10*time.Minute)) {
		t.Errorf("follow: timeout %s format %s window %d since %s", o.timeout, o.format, o.window, o.filter.Since)
	}
}

func TestRecordDeployKeysTheRepoProjectAndDevice(t *testing.T) {
	telemetryEnv(t, &fakeGraph{})
	repo := driverRepo(t)
	prev := flagProjectDir
	flagProjectDir = repo
	t.Cleanup(func() { flagProjectDir = prev })

	recordDeploy(orgctx.Device{ID: "dev-1", Name: "bench-a", DeviceID: "000FFF9BB74C"}, "smart_tv.c4z", "2026110401")

	rec, ok := deployrecord.Latest("smart-tv", "dev-1")
	if !ok {
		t.Fatal("no record for the deploy")
	}
	want := deployrecord.Record{
		Org: "acme", Project: "smart-tv", Driver: "smart_tv.c4z", Version: "2026110401",
		Device:     deployrecord.Device{ID: "dev-1", Name: "bench-a", DeviceID: "000FFF9BB74C"},
		DeployedAt: rec.DeployedAt,
	}
	if rec != want {
		t.Errorf("record = %+v, want %+v", rec, want)
	}
}
