package gitrepo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOrSkip skips when git isn't available on PATH.
func gitOrSkip(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
}

func TestRootOutsideRepoIsFalse(t *testing.T) {
	gitOrSkip(t)
	if _, ok := Root(context.Background(), t.TempDir()); ok {
		t.Fatal("a fresh temp dir should not be a git repo")
	}
}

func TestHasCommits(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()

	if out, err := run(ctx, dir, "init"); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	// Freshly init'd: unborn branch, no commits.
	if HasCommits(ctx, dir) {
		t.Error("a freshly init'd repo should report no commits")
	}

	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if !HasCommits(ctx, dir) {
		t.Error("repo with a commit should report commits")
	}
}

func TestIsClean(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()

	mustRun := func(args ...string) {
		t.Helper()
		if out, err := run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustRun("init")
	mustRun("config", "user.email", "test@example.com")
	mustRun("config", "user.name", "Test")

	// Unborn repo with an untracked file → not clean.
	write("driver.lua", "x")
	if clean, _ := IsClean(ctx, dir); clean {
		t.Error("repo with an untracked file should be unclean")
	}

	// After committing everything → clean.
	mustRun("add", "-A")
	mustRun("commit", "-m", "init")
	if clean, err := IsClean(ctx, dir); err != nil || !clean {
		t.Errorf("committed repo should be clean (clean=%v err=%v)", clean, err)
	}

	// A tracked modification → not clean.
	write("driver.lua", "y")
	if clean, _ := IsClean(ctx, dir); clean {
		t.Error("modified tracked file should be unclean")
	}
}

func TestBranchLifecycle(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()

	// Init a repo with one commit so HEAD and branches exist.
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	root, ok := Root(ctx, dir)
	if !ok || root == "" {
		t.Fatal("expected to detect the repo root")
	}

	if BranchExists(ctx, dir, "driverforge-init") {
		t.Fatal("driverforge-init should not exist yet")
	}
	if err := CreateBranch(ctx, dir, "driverforge-init"); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if !BranchExists(ctx, dir, "driverforge-init") {
		t.Fatal("driverforge-init should exist after creation")
	}
	if got := CurrentBranch(ctx, dir); got != "driverforge-init" {
		t.Errorf("CurrentBranch = %q, want driverforge-init", got)
	}
}

func TestValidBranchName(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()
	cases := []struct {
		name  string
		valid bool
	}{
		{"driverforge-init", true},
		{"feature/driverforge", true},
		{"my branch", false},
		{"feat..x", false},
		{"-leading-dash", false},
		{"trailing.lock", false},
	}
	for _, tc := range cases {
		if got := ValidBranchName(ctx, dir, tc.name); got != tc.valid {
			t.Errorf("ValidBranchName(%q) = %v, want %v", tc.name, got, tc.valid)
		}
	}
}

// CreateBranch failures must carry git's own message, not a bare exit status.
func TestCreateBranchErrorIncludesGitMessage(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	err := CreateBranch(ctx, dir, "bad name")
	if err == nil {
		t.Fatal("expected CreateBranch to fail for an invalid name")
	}
	if !strings.Contains(err.Error(), "bad name") || err.Error() == "exit status 1" {
		t.Errorf("error should carry git's message, got: %v", err)
	}
}

// Commit stages exactly the given paths and commits them with the message.
func TestCommit(t *testing.T) {
	gitOrSkip(t)
	ctx := context.Background()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		if out, err := run(ctx, dir, args...); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	committed := filepath.Join(dir, "committed.txt")
	stray := filepath.Join(dir, "stray.txt")
	for _, p := range []string{committed, stray} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := Commit(ctx, dir, "chore: add Anvil developer tooling support", []string{committed}); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	subject, err := run(ctx, dir, "log", "-1", "--pretty=%s")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(subject) != "chore: add Anvil developer tooling support" {
		t.Errorf("commit subject = %q", strings.TrimSpace(subject))
	}
	files, err := run(ctx, dir, "show", "--name-only", "--pretty=format:", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files, "committed.txt") || strings.Contains(files, "stray.txt") {
		t.Errorf("commit should contain exactly the given paths, got:\n%s", files)
	}
	// The stray file stays uncommitted in the work tree.
	if clean, _ := IsClean(ctx, dir); clean {
		t.Error("the stray file should remain uncommitted")
	}
}
