// Package gitrepo is a thin wrapper over the git CLI for the few operations
// `driverforge init` needs: detecting a work tree and creating a branch for the
// changes it's about to make. Every call degrades gracefully when git is
// unavailable or the directory isn't a repo, so callers can treat "no git" as a
// skip rather than an error.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Root returns the work-tree root containing dir. ok is false when dir isn't in
// a git repository or the git binary isn't available.
func Root(ctx context.Context, dir string) (root string, ok bool) {
	out, err := run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", false
	}
	root = strings.TrimSpace(out)
	return root, root != ""
}

// IsClean reports whether the work tree has no pending changes — nothing staged,
// modified, or untracked (ignored files don't count). On an unborn repo, any
// present-but-uncommitted files make it unclean. err is set only if git couldn't
// be run.
func IsClean(ctx context.Context, dir string) (bool, error) {
	out, err := run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// HasCommits reports whether the repo containing dir has at least one commit
// (HEAD resolves). A freshly `git init`'d repo has an unborn branch with none —
// `git checkout -b` there just renames the unborn branch rather than forking
// history, so callers should not offer to create a branch in that case.
func HasCommits(ctx context.Context, dir string) bool {
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// CurrentBranch returns the checked-out branch name, or "HEAD" when detached.
func CurrentBranch(ctx context.Context, dir string) string {
	out, err := run(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// BranchExists reports whether a local branch with the given name exists.
func BranchExists(ctx context.Context, dir, name string) bool {
	_, err := run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// ValidBranchName reports whether git accepts name as a branch name, via
// `git check-ref-format --branch`. It returns true when git can't be run at
// all — the caller's CreateBranch will surface the real failure then.
func ValidBranchName(ctx context.Context, dir, name string) bool {
	cmd := exec.CommandContext(ctx, "git", "check-ref-format", "--branch", name)
	cmd.Dir = dir
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	return true // ran fine, or git itself is unavailable
}

// CreateBranch creates and checks out a new branch, carrying any uncommitted
// working-tree changes onto it. Failures include git's own message, not just
// the exit status.
func CreateBranch(ctx context.Context, dir, name string) error {
	out, err := run(ctx, dir, "checkout", "-b", name)
	if err != nil {
		return fmt.Errorf("git checkout -b %s: %s: %w", name, strings.TrimSpace(out), err)
	}
	return nil
}

// Commit stages the given paths and commits them with message. Failures
// include git's own message, not just the exit status.
func Commit(ctx context.Context, dir, message string, paths []string) error {
	addArgs := append([]string{"add", "--"}, paths...)
	if out, err := run(ctx, dir, addArgs...); err != nil {
		return fmt.Errorf("git add: %s: %w", strings.TrimSpace(out), err)
	}
	if out, err := run(ctx, dir, "commit", "-m", message); err != nil {
		return fmt.Errorf("git commit: %s: %w", strings.TrimSpace(out), err)
	}
	return nil
}

func run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}
