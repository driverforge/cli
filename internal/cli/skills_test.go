package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/skills"
	"github.com/driverforge/cli/internal/telemetryread"
)

// The skills name commands, flags, codes, fields and exit statuses, and an
// agent acts on them literally. These tests hold every skill to the CLI it
// ships in, so a rename here fails the build instead of misleading an agent.

func skillFiles(t *testing.T) map[string]string {
	t.Helper()
	list, err := skills.List()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range list {
		files, err := skills.Files(s.Name, "dev")
		if err != nil {
			t.Fatal(err)
		}
		for rel, b := range files {
			out[s.Name+"/"+rel] = string(b)
		}
	}
	return out
}

var (
	codeSpanRE = regexp.MustCompile("`([^`\n]+)`")
	fenceRE    = regexp.MustCompile("(?s)```[a-z]*\n(.*?)```")
	// invocationEnd is where a command in a shell line stops.
	invocationEnd = regexp.MustCompile(`\s(\||>|2>|#|\\|&&|;)|\)$`)
)

// invocations returns every `driverforge …` command a document shows: in
// inline code and in fenced blocks.
func invocations(doc string) []string {
	var out []string
	add := func(s string) {
		i := strings.Index(s, "driverforge ")
		if i < 0 {
			return
		}
		s = s[i:]
		if loc := invocationEnd.FindStringIndex(s); loc != nil {
			s = s[:loc[0]]
		}
		out = append(out, strings.TrimSpace(s))
	}
	for _, m := range fenceRE.FindAllStringSubmatch(doc, -1) {
		for _, line := range strings.Split(m[1], "\n") {
			add(line)
		}
	}
	prose := fenceRE.ReplaceAllString(doc, "")
	for _, m := range codeSpanRE.FindAllStringSubmatch(prose, -1) {
		if strings.HasPrefix(m[1], "driverforge ") {
			add(m[1])
		}
	}
	return out
}

func TestSkillsNameOnlyRealCommandsAndFlags(t *testing.T) {
	root := newRootCmd()
	root.InitDefaultHelpFlag()
	root.InitDefaultVersionFlag()
	seen := 0
	for file, doc := range skillFiles(t) {
		for _, inv := range invocations(doc) {
			words := strings.Fields(strings.TrimPrefix(inv, "driverforge"))
			if len(words) > 0 && strings.HasPrefix(words[0], "<") {
				continue // a placeholder: `driverforge <command> --help`
			}
			// `driverforge logs|errors|events` names several commands at once.
			var alternatives []string
			if len(words) > 0 && strings.Contains(words[0], "|") {
				alternatives = strings.Split(words[0], "|")
			} else {
				alternatives = []string{""}
			}
			for _, alt := range alternatives {
				w := words
				if alt != "" {
					w = append([]string{alt}, words[1:]...)
				}
				checkInvocation(t, root, file, inv, w)
				seen++
			}
		}
	}
	if seen < 20 {
		t.Fatalf("found only %d driverforge invocations; is the extractor broken?", seen)
	}
}

func checkInvocation(t *testing.T, root *cobra.Command, file, inv string, words []string) {
	t.Helper()
	cmd, rest, err := root.Find(words)
	if err != nil {
		t.Errorf("%s: %q: %v", file, inv, err)
		return
	}
	if cmd == root && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		t.Errorf("%s: %q: no command %q", file, inv, rest[0])
		return
	}
	if cmd != root && !cmd.Runnable() {
		t.Errorf("%s: %q: %q needs a subcommand", file, inv, cmd.CommandPath())
	}
	for _, w := range rest {
		if !strings.HasPrefix(w, "-") || w == "-" {
			continue
		}
		name, _, _ := strings.Cut(strings.TrimLeft(w, "-"), "=")
		var f *pflag.Flag
		if strings.HasPrefix(w, "--") {
			f = lookupFlag(cmd, name)
		} else {
			f = lookupShorthand(cmd, name)
		}
		if f == nil {
			t.Errorf("%s: %q: %s has no flag %s", file, inv, cmd.CommandPath(), w)
		}
	}
}

func lookupFlag(cmd *cobra.Command, name string) *pflag.Flag {
	if f := cmd.Flags().Lookup(name); f != nil {
		return f
	}
	return cmd.InheritedFlags().Lookup(name)
}

func lookupShorthand(cmd *cobra.Command, s string) *pflag.Flag {
	if f := cmd.Flags().ShorthandLookup(s); f != nil {
		return f
	}
	return cmd.InheritedFlags().ShorthandLookup(s)
}

// TestSkillsDocumentEveryTelemetryFlag keeps commands.md complete: a flag added
// to a reader must be documented for agents too.
func TestSkillsDocumentEveryTelemetryFlag(t *testing.T) {
	doc := skillFiles(t)["anvil-driver-dev-cycle/references/commands.md"]
	if doc == "" {
		t.Fatal("commands.md is missing")
	}
	root := newRootCmd()
	for _, name := range []string{"logs", "errors", "events", "get"} {
		cmd, _, err := root.Find([]string{name})
		if err != nil {
			t.Fatal(err)
		}
		cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
			if !strings.Contains(doc, "`--"+f.Name) {
				t.Errorf("commands.md doesn't document %s --%s", name, f.Name)
			}
		})
	}
}

// snakeRE matches a snake_case name: an error or warning code, or a field.
var snakeRE = regexp.MustCompile(`^[a-z]+(_[a-z]+)+$`)

// TestSkillsNameOnlyRealCodesAndFields checks every snake_case name a skill
// puts in code — `wait_timeout`, `entries[].correlation_id` — against the
// CLI's codes and the JSON fields of its output.
func TestSkillsNameOnlyRealCodesAndFields(t *testing.T) {
	known := map[string]bool{}
	for _, c := range []string{
		codeUsage, codeInputRequired, codeAuthRequired, codeInterrupted, codeRateLimited,
		codeConnectionLost, codeUnavailable, codeInternal,
		codeContextUnresolved, codeContextAmbiguous, codeProjectNotFound,
		codeDeviceNotFound, codeEntryNotFound, codeNotInitialised,
		codeBuildFailed, codeAgentUnreachable, codeDeployFailed, codeDeployTimedOut,
		warnNoEntries, warnRetentionClamped, warnPayloadTruncated, warnTailClamped,
		telemetryread.ReasonUntilMatch, telemetryread.ReasonMinEvents,
		string(agent.StateConfirmedWithErrors),
		"wait_timeout", // runWait's timeout error
		// details keys
		"retry_after_ms", "window_minutes",
	} {
		known[c] = true
	}
	for _, v := range []any{envelope{}, scopeOut{}, waitOut{}, warning{}, cmdErr{}, truncatedRef{},
		telemetryread.Truncated{}, shipOut{}, initOut{}, initProjectOut{}, initChangeOut{}} {
		for _, f := range jsonFields(reflect.TypeOf(v)) {
			known[f] = true
		}
	}
	for _, k := range telemetryread.AllowedKeys {
		known[k] = true
	}

	for file, doc := range skillFiles(t) {
		for _, m := range codeSpanRE.FindAllStringSubmatch(doc, -1) {
			if strings.HasPrefix(m[1], "driverforge ") {
				continue // flags, checked above
			}
			for _, seg := range regexp.MustCompile(`[^a-z_]+`).Split(m[1], -1) {
				if snakeRE.MatchString(seg) && !known[seg] {
					t.Errorf("%s names %q, which the CLI doesn't emit", file, seg)
				}
			}
		}
	}
}

func jsonFields(typ reflect.Type) []string {
	var out []string
	for i := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}

// TestSkillsExitStatusesMatch checks output-shapes.md's code table against
// the exit status each code really has, and every "exit N" against the
// statuses that exist.
func TestSkillsExitStatusesMatch(t *testing.T) {
	statuses := []int{0, 1, exitUsage, exitWaitTimeout, exitAuth, exitContext, exitRateLimited, exitInterrupted}
	files := skillFiles(t)
	for file, doc := range files {
		for _, m := range regexp.MustCompile(`(?i)\bexit (\d+)`).FindAllStringSubmatch(doc, -1) {
			n, _ := strconv.Atoi(m[1])
			if !slices.Contains(statuses, n) {
				t.Errorf("%s names exit %d, which no command returns", file, n)
			}
		}
	}

	row := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| (\\d+) \\|")
	rows := row.FindAllStringSubmatch(files["anvil-driver-dev-cycle/references/output-shapes.md"], -1)
	if len(rows) < 10 {
		t.Fatalf("found %d code rows in output-shapes.md; is the table's format changed?", len(rows))
	}
	for _, r := range rows {
		want, _ := strconv.Atoi(r[2])
		if got := (&cmdErr{Code: r[1]}).exit(); got != want {
			t.Errorf("output-shapes.md says %s exits %d; it exits %d", r[1], want, got)
		}
	}
}

func TestSkillsInstallStructured(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	dir := filepath.Join(t.TempDir(), "skills")
	stdout, stderr, code := run(t, t.TempDir(), "skills", "install", "--path", dir)
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	doc := decode(t, stdout)
	got := doc["skills"].([]any)[0].(map[string]any)
	if got["name"] != "anvil-driver-dev-cycle" || got["action"] != "installed" {
		t.Fatalf("skills[0] = %v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "anvil-driver-dev-cycle", "SKILL.md")); err != nil {
		t.Fatal(err)
	}

	stdout, _, _ = run(t, t.TempDir(), "skills", "install", "--path", dir)
	if a := decode(t, stdout)["skills"].([]any)[0].(map[string]any)["action"]; a != "unchanged" {
		t.Errorf("reinstall action = %v, want unchanged", a)
	}

	stdout, code = runFailing(t, "skills", "install", "nope", "--path", dir)
	if code != 1 || decode(t, stdout)["error"].(map[string]any)["code"] != codeSkillNotFound {
		t.Errorf("unknown skill: exit %d, %s", code, stdout)
	}
}

// With no flag, skills install at the root of the git repository, even from
// a subdirectory.
func TestSkillsInstallDefaultsToTheRepoRoot(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	sub := filepath.Join(repo, "drivers", "pump")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := run(t, sub, "skills", "install")
	if code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	want, _ := filepath.EvalSymlinks(filepath.Join(repo, ".agents", "skills"))
	got, _ := filepath.EvalSymlinks(decode(t, stdout)["dir"].(string))
	if got != want {
		t.Errorf("dir = %s, want %s", got, want)
	}
}

// --global installs into ~/.agents/skills.
func TestSkillsInstallGlobal(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	home := t.TempDir()
	t.Setenv("HOME", home)
	stdout, _, code := run(t, t.TempDir(), "skills", "install", "--global")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "anvil-driver-dev-cycle", "SKILL.md")); err != nil {
		t.Error(err)
	}
}

func TestSkillsInstallOutsideARepo(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	stdout, code := runFailing(t, "--project-dir", t.TempDir(), "skills", "install")
	if code != exitContext || decode(t, stdout)["error"].(map[string]any)["code"] != codeNotAProject {
		t.Errorf("exit %d, %s", code, stdout)
	}
}

// runFailing runs a command expected to fail under structured output and
// returns the error document Execute would write, and the exit status.
func runFailing(t *testing.T, args ...string) (string, int) {
	t.Helper()
	root := newRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"--no-update-check"}, args...))
	if _, err := root.ExecuteC(); err != nil {
		code := writeFailure(&stdout, &stderr, err)
		return stdout.String(), code
	}
	t.Fatalf("command succeeded: %s", stdout.String())
	return "", 0
}

func TestSkillsInstallGlobalAndPathConflict(t *testing.T) {
	t.Setenv("DRIVERFORGE_OUTPUT", "json")
	stdout, code := runFailing(t, "skills", "install", "--global", "--path", t.TempDir())
	if code != exitUsage || decode(t, stdout)["error"].(map[string]any)["code"] != codeUsage {
		t.Errorf("exit %d, %s", code, stdout)
	}
}
