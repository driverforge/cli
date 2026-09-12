package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/gitrepo"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sdkChanges plans the two files the SDK update writes: a modified vendor
// bundle and a modified config, against an initialised project.
func sdkChanges(t *testing.T, dir string) []initdriver.Change {
	t.Helper()
	src := filepath.Join(dir, "src")
	var changes []initdriver.Change
	for _, f := range []struct{ abs, body string }{
		{initdriver.VendorBundlePath(src), "-- sdk 0.5.0\n"},
		{filepath.Join(dir, ".driverforge", "config.json"), `{"sdkVersion":"0.5.0"}` + "\n"},
	} {
		ch, ok, err := fileChange(dir, f.abs, []byte(f.body))
		if err != nil || !ok {
			t.Fatalf("fileChange(%s) = ok %v, err %v", f.abs, ok, err)
		}
		changes = append(changes, ch)
	}
	return changes
}

// With a branch and a message, the executor forks the branch, writes the
// files and commits exactly those paths there — the tree ends clean and the
// original branch is untouched, the same shape init leaves behind.
func TestExecuteFileChangesBranchesAndCommits(t *testing.T) {
	dir := initialisedProject(t, "0.3.0")
	gitIdentity(t) // gitrepo.Commit uses the ambient identity; CI runners may have none
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	ctx := context.Background()
	base := gitrepo.CurrentBranch(ctx, dir)
	baseHead := gitOut(t, dir, "rev-parse", "HEAD")
	branch, msg := sdkUpdateBranchDefaults("0.5.0")

	if err := executeFileChanges(ctx, io.Discard, proj, branch, msg, sdkChanges(t, dir)); err != nil {
		t.Fatal(err)
	}

	if got := gitrepo.CurrentBranch(ctx, dir); got != "driverforge-sdk-0.5.0" {
		t.Errorf("current branch = %q, want driverforge-sdk-0.5.0", got)
	}
	if clean, _ := gitrepo.IsClean(ctx, dir); !clean {
		t.Errorf("tree not clean after commit:\n%s", gitOut(t, dir, "status", "--porcelain"))
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "chore: update Driverforge SDK to 0.5.0" {
		t.Errorf("commit subject = %q", got)
	}
	files := gitOut(t, dir, "show", "--name-only", "--format=", "HEAD")
	for _, want := range []string{"src/vendor/", ".driverforge/config.json"} {
		if !strings.Contains(files, want) {
			t.Errorf("commit missing %s:\n%s", want, files)
		}
	}
	if got := gitOut(t, dir, "rev-parse", base); got != baseHead {
		t.Errorf("%s moved from %s to %s; the upgrade must land only on its branch", base, baseHead, got)
	}
}

// Branch but no message: the files are written on the new branch and left
// uncommitted for the user to review.
func TestExecuteFileChangesBranchWithoutCommit(t *testing.T) {
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	ctx := context.Background()

	if err := executeFileChanges(ctx, io.Discard, proj, "sdk-bump", "", sdkChanges(t, dir)); err != nil {
		t.Fatal(err)
	}
	if got := gitrepo.CurrentBranch(ctx, dir); got != "sdk-bump" {
		t.Errorf("current branch = %q, want sdk-bump", got)
	}
	if clean, _ := gitrepo.IsClean(ctx, dir); clean {
		t.Error("changes should be left uncommitted when no message is given")
	}
	if body, _ := os.ReadFile(initdriver.VendorBundlePath(proj.SrcDir)); string(body) != "-- sdk 0.5.0\n" {
		t.Errorf("bundle not written: %q", body)
	}
}

// No branch: unchanged behaviour — files land on the current branch.
func TestExecuteFileChangesWithoutBranch(t *testing.T) {
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	ctx := context.Background()
	base := gitrepo.CurrentBranch(ctx, dir)

	if err := executeFileChanges(ctx, io.Discard, proj, "", "", sdkChanges(t, dir)); err != nil {
		t.Fatal(err)
	}
	if got := gitrepo.CurrentBranch(ctx, dir); got != base {
		t.Errorf("branch changed to %q without being asked", got)
	}
	if clean, _ := gitrepo.IsClean(ctx, dir); clean {
		t.Error("changes should be in the working tree")
	}
}

func TestSDKUpdateBranchDefaults(t *testing.T) {
	b, m := sdkUpdateBranchDefaults("1.2.3")
	if b != "driverforge-sdk-1.2.3" || m != "chore: update Driverforge SDK to 1.2.3" {
		t.Errorf("defaults = %q, %q", b, m)
	}
}

// stubBranchIntent replaces the interactive wizard for one test, recording the
// defaults it was offered and answering with the given branch and message.
func stubBranchIntent(t *testing.T, branch, msg string, err error) *[2]string {
	t.Helper()
	old := branchIntent
	t.Cleanup(func() { branchIntent = old })
	var offered [2]string
	branchIntent = func(_ context.Context, _ io.Writer, _, defBranch, defMsg string) (string, string, error) {
		offered = [2]string{defBranch, defMsg}
		return branch, msg, err
	}
	return &offered
}

// cachedSDK places a bundle for version in the per-user SDK cache under a temp
// home, so fetchSDK is a cache hit and the update never touches the network.
func cachedSDK(t *testing.T, version, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	p, err := sdkrelease.CachedPath(version)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitIdentity(t *testing.T) {
	t.Helper()
	for _, kv := range [][2]string{
		{"GIT_AUTHOR_NAME", "t"}, {"GIT_AUTHOR_EMAIL", "t@example.com"},
		{"GIT_COMMITTER_NAME", "t"}, {"GIT_COMMITTER_EMAIL", "t@example.com"},
	} {
		t.Setenv(kv[0], kv[1])
	}
}

// The whole SDK update on a clean project: the wizard is offered the
// version-specific defaults, and accepting them lands the bundle and the
// config bump as one commit on the new branch.
func TestRunSDKUpdateBranchesAndCommits(t *testing.T) {
	gitIdentity(t)
	cachedSDK(t, "0.5.0", "-- sdk 0.5.0\n")
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	offered := stubBranchIntent(t, "driverforge-sdk-0.5.0", "chore: update Driverforge SDK to 0.5.0", nil)
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	cfg := &driverforgeconfig.Config{SDKVersion: "0.3.0"}
	var out bytes.Buffer
	ctx := context.Background()

	if err := runSDKUpdate(ctx, &out, proj, cfg, "0.5.0"); err != nil {
		t.Fatalf("runSDKUpdate: %v\n%s", err, out.String())
	}

	if want := [2]string{"driverforge-sdk-0.5.0", "chore: update Driverforge SDK to 0.5.0"}; *offered != want {
		t.Errorf("wizard offered %v, want %v", *offered, want)
	}
	if got := gitrepo.CurrentBranch(ctx, dir); got != "driverforge-sdk-0.5.0" {
		t.Errorf("current branch = %q", got)
	}
	if clean, _ := gitrepo.IsClean(ctx, dir); !clean {
		t.Errorf("tree not clean after commit:\n%s", gitOut(t, dir, "status", "--porcelain"))
	}
	if got := gitOut(t, dir, "log", "-1", "--format=%s"); got != "chore: update Driverforge SDK to 0.5.0" {
		t.Errorf("commit subject = %q", got)
	}
	got, err := driverforgeconfig.Load(dir)
	if err != nil || got.SDKVersion != "0.5.0" {
		t.Errorf("config = %+v, %v; want sdkVersion 0.5.0", got, err)
	}
	if body, _ := os.ReadFile(initdriver.VendorBundlePath(proj.SrcDir)); string(body) != "-- sdk 0.5.0\n" {
		t.Errorf("bundle = %q", body)
	}
	if !strings.Contains(out.String(), "Updated to Driverforge SDK 0.5.0") {
		t.Errorf("output missing the success line:\n%s", out.String())
	}
}

// Declining the branch keeps the old behaviour: edits on the current branch.
func TestRunSDKUpdateWithoutBranch(t *testing.T) {
	cachedSDK(t, "0.5.0", "-- sdk 0.5.0\n")
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	stubBranchIntent(t, "", "", nil)
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	ctx := context.Background()
	base := gitrepo.CurrentBranch(ctx, dir)

	if err := runSDKUpdate(ctx, io.Discard, proj, &driverforgeconfig.Config{SDKVersion: "0.3.0"}, "0.5.0"); err != nil {
		t.Fatal(err)
	}
	if got := gitrepo.CurrentBranch(ctx, dir); got != base {
		t.Errorf("branch = %q, want to stay on %q", got, base)
	}
	if clean, _ := gitrepo.IsClean(ctx, dir); clean {
		t.Error("edits should be left in the working tree")
	}
}

// A wizard error (an invalid or existing branch name) aborts before any file
// is written.
func TestRunSDKUpdateWizardErrorWritesNothing(t *testing.T) {
	cachedSDK(t, "0.5.0", "-- sdk 0.5.0\n")
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	stubBranchIntent(t, "", "", errors.New("branch already exists"))
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}

	err := runSDKUpdate(context.Background(), io.Discard, proj, &driverforgeconfig.Config{SDKVersion: "0.3.0"}, "0.5.0")
	if err == nil || !strings.Contains(err.Error(), "branch already exists") {
		t.Fatalf("err = %v", err)
	}
	if clean, _ := gitrepo.IsClean(context.Background(), dir); !clean {
		t.Error("nothing should have been written")
	}
}

// Nothing to change means nothing to ask: the wizard is never consulted.
func TestRunSDKUpdateNoChangesSkipsWizard(t *testing.T) {
	cachedSDK(t, "0.3.0", "-- sdk\n") // identical to the initialised project's bundle
	dir := initialisedProject(t, "0.3.0")
	oldNoTUI := flagNoTUI
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = oldNoTUI })
	stubBranchIntent(t, "", "", errors.New("wizard must not run"))
	proj := project.Project{Dir: dir, SrcDir: filepath.Join(dir, "src")}
	var out bytes.Buffer

	if err := runSDKUpdate(context.Background(), &out, proj, &driverforgeconfig.Config{SDKVersion: "0.3.0"}, "0.3.0"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "nothing to update") {
		t.Errorf("output:\n%s", out.String())
	}
}
