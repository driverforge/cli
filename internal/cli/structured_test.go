package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/deploycache"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

// jsonLines decodes every line of s, failing the test on one that isn't JSON.
func jsonLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line isn't JSON: %q", line)
		}
		out = append(out, m)
	}
	return out
}

func progressOf(t *testing.T, m map[string]any) (status, message string) {
	t.Helper()
	p, ok := m["progress"].(map[string]any)
	if !ok {
		t.Fatalf("not a progress line: %v", m)
	}
	return p["status"].(string), p["message"].(string)
}

func TestProgressWriterTurnsTextIntoProgressLines(t *testing.T) {
	var buf bytes.Buffer
	pw := newProgressWriter(&buf)
	fmt.Fprint(pw, "\n✓ Built")
	fmt.Fprint(pw, " (12ms)\n\x1b[33m⚠ old agent\x1b[0m\n  – Encrypt\n┃ ─────\n┃  controller  bench-a\nplain words\nWaiting for authorization…\n")

	lines := jsonLines(t, buf.String())
	want := [][2]string{
		{statusOK, "Built (12ms)"},
		{statusWarning, "old agent"},
		{statusSkipped, "Encrypt"},
		{statusInfo, "controller  bench-a"},
		{statusInfo, "plain words"},
		{statusRunning, "Waiting for authorization"},
	}
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d:\n%s", len(lines), len(want), buf.String())
	}
	for i, w := range want {
		if status, msg := progressOf(t, lines[i]); status != w[0] || msg != w[1] {
			t.Errorf("line %d = %s %q, want %s %q", i, status, msg, w[0], w[1])
		}
	}
	if got := pw.reported(); len(got) != 1 || got[0].Message != "old agent" {
		t.Errorf("reported warnings = %+v, want the one warning", got)
	}
}

func TestProgressFrameSteps(t *testing.T) {
	var buf bytes.Buffer
	f := progressFrame{newProgressWriter(&buf)}
	f.Step("Connecting to bench-a…")
	f.Step("Build complete")
	f.Logf("✗ %s", "Package")
	f.Warnf("⚠ %s", "unverified")
	f.Row("controller", "bench-a") // reported in the result, not as progress

	var got []string
	for _, l := range jsonLines(t, buf.String()) {
		status, msg := progressOf(t, l)
		got = append(got, status+": "+msg)
	}
	want := []string{"running: Connecting to bench-a", "ok: Build complete", "failed: Package", "warning: unverified"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("progress = %q, want %q", got, want)
	}
}

func TestWriteFailure(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		code     string
		exit     int
		silent   bool
		internal bool
	}{
		{"coded", clierr.WithCode(clierr.User("device \"x\" not found", "run `driverforge device list`"), codeDeviceNotFound), codeDeviceNotFound, exitContext, false, false},
		{"explicit exit wins", clierr.WithExit(clierr.WithCode(clierr.User("m", ""), codeDeviceNotFound), 7), codeDeviceNotFound, 7, false, false},
		{"uncoded", clierr.User("couldn't do it", ""), codeFailed, 1, false, false},
		{"auth sentinel", fmt.Errorf("wrapped: %w", auth.ErrNotLoggedIn), codeAuthRequired, exitAuth, false, false},
		{"silent", silentExit(3), "", 3, true, false},
		{"bug", errors.New("nil map"), codeInternal, 1, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := writeFailure(&stdout, &stderr, tc.err); exit != tc.exit {
				t.Errorf("exit = %d, want %d", exit, tc.exit)
			}
			if tc.silent {
				if stdout.Len()+stderr.Len() != 0 {
					t.Errorf("a silent failure wrote output: %q %q", stdout.String(), stderr.String())
				}
				return
			}
			doc := decode(t, stdout.String())
			e := doc["error"].(map[string]any)
			if e["code"] != tc.code {
				t.Errorf("code = %v, want %s", e["code"], tc.code)
			}
			if tc.internal && strings.Contains(stdout.String(), "nil map") {
				t.Error("an unexpected error's text reached the output")
			}
			if lines := jsonLines(t, stderr.String()); len(lines) != 1 || lines[0]["error"] == nil {
				t.Errorf("stderr = %q, want one error line", stderr.String())
			}
		})
	}
}

func TestSentence(t *testing.T) {
	for in, want := range map[string]string{
		"no device is selected":                "No device is selected.",
		"you're not logged in":                 "You're not logged in.",
		"config.nope.lua not found in src":     "config.nope.lua not found in src.",
		"driverforge init needs the SDK":       "driverforge init needs the SDK.",
		"Already a sentence.":                  "Already a sentence.",
		"run `driverforge device select <id>`": "Run `driverforge device select <id>`",
		"--version needs a value":              "--version needs a value.",
	} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPromptsRefuseUnderStructuredOutput(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	_, err := ui.Confirm("Run a full deploy now?", "", true)
	var ue *clierr.UserError
	if !errors.As(err, &ue) || ue.Code != codeInputRequired || !strings.Contains(ue.Message, "Run a full deploy now?") {
		t.Fatalf("Confirm = %v, want input_required naming the question", err)
	}
	if ui.Interactive(false) {
		t.Error("Interactive() under structured output")
	}
}

func TestStructuredBuildEmitsOneDocument(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	stdout, stderr := runStructured(t, writeBuildableProject(t), newBuildCmd())
	doc := decode(t, stdout)
	if doc["driver"] != "test" || doc["configuration"] != "default" || !strings.HasSuffix(doc["artifact"].(string), "test.c4z") {
		t.Errorf("build document = %v", doc)
	}
	if lines := jsonLines(t, stderr); len(lines) == 0 {
		t.Error("no progress on stderr")
	}
}

func TestStructuredSync(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	f := newFakeAgent()
	setupSyncEnv(t, f.start(t))
	projDir := writeBuildableProject(t)
	writeDriverforgeConfig(t, projDir)

	// No baseline: a sync can't hot-swap, and there's nobody to confirm a
	// deploy, so it refuses with deploy_required rather than prompting.
	cmd := newSyncCmd()
	_, _, err := runStructuredErr(t, projDir, cmd)
	var ue *clierr.UserError
	if !errors.As(err, &ue) || ue.Code != codeDeployRequired {
		t.Fatalf("sync without a baseline = %v, want deploy_required", err)
	}

	built, err := readC4zEntry(filepath.Join(projDir, "dist", "test.c4z"), "driver.xml")
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := driverxml.StructuralHash(built)
	f.setBaseline("test.c4z", hash)
	_ = deploycache.Set("d1", "test.c4z", hash)

	stdout, stderr := runStructured(t, projDir, newSyncCmd())
	doc := decode(t, stdout)
	if doc["action"] != "sync" || doc["state"] != "synced" || doc["verified"] != true {
		t.Errorf("sync document = %v", doc)
	}
	if dev := doc["device"].(map[string]any); dev["id"] != "d1" {
		t.Errorf("device = %v", dev)
	}
	if inst, _ := doc["instances"].([]any); len(inst) != 1 || inst[0] != "Ctrl" {
		t.Errorf("instances = %v", doc["instances"])
	}
	for _, l := range jsonLines(t, stderr) {
		if l["progress"] == nil {
			t.Errorf("stderr carries a non-progress line: %v", l)
		}
	}
}

// runStructured runs cmd against projDir and returns its stdout and stderr,
// failing the test on an error.
func runStructured(t *testing.T, projDir string, cmd interface {
	SetOut(w io.Writer)
	SetErr(w io.Writer)
	SetArgs([]string)
	ExecuteContext(ctx context.Context) error
}) (string, string) {
	t.Helper()
	stdout, stderr, err := runStructuredErr(t, projDir, cmd)
	if err != nil {
		t.Fatalf("command failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	return stdout, stderr
}

func runStructuredErr(t *testing.T, projDir string, cmd interface {
	SetOut(w io.Writer)
	SetErr(w io.Writer)
	SetArgs([]string)
	ExecuteContext(ctx context.Context) error
}) (string, string, error) {
	t.Helper()
	old := flagProjectDir
	flagProjectDir = projDir
	t.Cleanup(func() { flagProjectDir = old })
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{})
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), err
}
