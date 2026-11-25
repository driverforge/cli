package scaffold

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/internal/clierr"
)

// clone shallow-clones a non-GitHub repository with the user's git and reads
// its files. The user's own credentials (ssh agent, credential helper) apply,
// so private repositories work; git never prompts, since a prompt would fight
// the progress display.
func clone(ctx context.Context, src Source) ([]File, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, clierr.User("git isn't installed", "cloning "+src.URL+" needs git; install it, or use a github.com repository")
	}
	tmp, err := os.MkdirTemp("", "driverforge-template-*")
	if err != nil {
		return nil, fmt.Errorf("template clone: %w", err)
	}
	defer os.RemoveAll(tmp)

	args := []string{"clone", "--quiet", "--depth", "1", "--no-tags"}
	if src.Ref != "" {
		args = append(args, "--branch", src.Ref)
	}
	dir := filepath.Join(tmp, "repo")
	args = append(args, "--", src.URL, dir)
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ALLOW_PROTOCOL=https:http:ssh:git:file", // no ext:: or other helper transports
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, clierr.User(fmt.Sprintf("couldn't clone %s", src),
			"check the URL, the branch or tag, and that git can access it without a prompt (ssh key or credential helper). git said: "+gitMessage(string(out)))
	}
	return readTree(dir)
}

// gitMessage condenses git's output to its fatal/error lines, dropping the
// "fatal: " prefix, so the user sees what git actually complained about.
func gitMessage(out string) string {
	var msgs []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		l = strings.TrimSpace(l)
		l = strings.TrimPrefix(strings.TrimPrefix(l, "fatal: "), "error: ")
		if l != "" {
			msgs = append(msgs, l)
		}
	}
	if len(msgs) == 0 {
		return "no output"
	}
	return strings.Join(msgs, " ")
}

// readTree loads the regular files under root, skipping .git, symlinks and
// anything over the template limits.
func readTree(root string) ([]File, error) {
	var (
		files []File
		total int64
	)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !safeRel(rel) {
			return nil
		}
		if info.Size() > maxFileSize {
			return clierr.User(fmt.Sprintf("template file %s is too large", rel), "templates are limited to small project files")
		}
		if total += info.Size(); total > maxTotalSize || len(files) >= maxFiles {
			return clierr.User("the template repository is too large", "point --repo at a smaller repository")
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		mode := fs.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		files = append(files, File{Path: rel, Data: data, Mode: mode})
		return nil
	})
	if err != nil {
		var ue *clierr.UserError
		if errors.As(err, &ue) {
			return nil, err
		}
		return nil, fmt.Errorf("template clone: reading repository: %w", err)
	}
	return files, nil
}
