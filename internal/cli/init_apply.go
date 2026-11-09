package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/gitrepo"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/ui"
)

// executeInit performs the confirmed plan as an ordered, live-progress sequence:
// create or link the project, create the git branch (if any), then write the
// files. The project token arrives with the project itself, so there is nothing
// to resolve or provision in between.
// It returns the linked or created project and the changes written.
func executeInit(ctx context.Context, out io.Writer, proj project.Project, cl *driverforgeapi.Client, org orgctx.Org, pp projectPlan, dp driverPlan, branch, commitMsg string) (driverforgeapi.Project, []initdriver.Change, error) {
	var (
		linked driverforgeapi.Project
		steps  []ui.Step
	)

	if pp.create != nil {
		ci := pp.create
		steps = append(steps, ui.Step{
			Label: fmt.Sprintf("Creating project %s", ci.name),
			Run: func() error {
				p, err := cl.CreateProject(ctx, driverforgeapi.CreateProjectInput{
					Name:           ci.name,
					Description:    ci.description,
					OrganizationID: org.ID,
					Platform:       ci.platform.ID,
				})
				if err != nil {
					return err
				}
				linked = p
				return nil
			},
		})
	} else {
		linked = *pp.existing
	}

	if branch != "" {
		steps = append(steps, ui.Step{
			Label: "Creating git branch " + branch,
			Run: func() error {
				root, ok := gitrepo.Root(ctx, proj.Dir)
				if !ok {
					return fmt.Errorf("not a git repository")
				}
				return gitrepo.CreateBranch(ctx, root, branch)
			},
		})
	}

	var written []initdriver.Change
	steps = append(steps, ui.Step{
		Label: "Applying file changes",
		Run: func() error {
			plan, err := buildInitChanges(proj, dp, org, linked, linked.Token)
			if err != nil {
				return err
			}
			written = plan.Changes
			return applyChanges(plan.Changes)
		},
	})

	// Commit exactly what was written (the project token is inlined by design —
	// it identifies the driver and authorises nothing; see initdriver.tokenArg),
	// so the branch carries the whole change as one revertable commit.
	if branch != "" && commitMsg != "" {
		steps = append(steps, ui.Step{
			Label: "Committing changes",
			Run: func() error {
				root, ok := gitrepo.Root(ctx, proj.Dir)
				if !ok {
					return fmt.Errorf("not a git repository")
				}
				paths := make([]string, 0, len(written))
				for _, c := range written {
					paths = append(paths, c.AbsPath)
				}
				return gitrepo.Commit(ctx, root, commitMsg, paths)
			},
		})
	}

	if err := ui.RunSteps(ctx, out, ui.Interactive(flagNoTUI), steps); err != nil {
		return linked, written, executeErr(err)
	}
	return linked, written, nil
}

// executeErr maps a failed execution step to a friendly error (plan limit /
// suspension / not-logged-in), leaving anything else as-is.
func executeErr(err error) error {
	switch {
	case errors.Is(err, driverforgeapi.ErrProjectLimit):
		return clierr.WithCode(clierr.WrapT(err, "Project limit reached",
			"your plan doesn't include creating another project",
			urlLine("Manage your plan", orgLinkURL("plan", "settings/subscription"))), codeProjectLimit)
	case errors.Is(err, driverforgeapi.ErrSubscriptionSuspended):
		return clierr.WithCode(clierr.WrapT(err, "Subscription suspended",
			"your organization's subscription is suspended",
			urlLine("Update billing", orgLinkURL("billing", "settings/billing"))), codeSubscriptionSuspended)
	}
	return loginAwareErr(err)
}

// executeFileChanges runs the confirmed file writes as a live-progress step
// list, optionally on a new git branch and, when commitMsg is also set,
// committed there as one revertable commit of exactly the written paths. It is
// the shared tail of doctor repairs and SDK updates (init has its own, whose
// changes are only known mid-step).
func executeFileChanges(ctx context.Context, out io.Writer, proj project.Project, branch, commitMsg string, changes []initdriver.Change) error {
	var steps []ui.Step
	if branch != "" {
		b := branch
		steps = append(steps, ui.Step{
			Label: "Creating git branch " + b,
			Run: func() error {
				root, ok := gitrepo.Root(ctx, proj.Dir)
				if !ok {
					return fmt.Errorf("not a git repository")
				}
				return gitrepo.CreateBranch(ctx, root, b)
			},
		})
	}
	ch := changes
	steps = append(steps, ui.Step{
		Label: "Applying file changes",
		Run:   func() error { return applyChanges(ch) },
	})
	if branch != "" && commitMsg != "" {
		msg := commitMsg
		steps = append(steps, ui.Step{
			Label: "Committing changes",
			Run: func() error {
				root, ok := gitrepo.Root(ctx, proj.Dir)
				if !ok {
					return fmt.Errorf("not a git repository")
				}
				paths := make([]string, 0, len(ch))
				for _, c := range ch {
					paths = append(paths, c.AbsPath)
				}
				return gitrepo.Commit(ctx, root, msg, paths)
			},
		})
	}
	if err := ui.RunSteps(ctx, out, ui.Interactive(flagNoTUI), steps); err != nil {
		return executeErr(err)
	}
	return nil
}

// plural returns "s" unless n is exactly 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func relTo(root, abs string) string {
	if r, err := filepath.Rel(root, abs); err == nil {
		return r
	}
	return abs
}

// applyChanges writes every planned change, backing up edited files to <file>.bak
// first and rolling everything back on the first failure. On success the backups
// are removed — the change is transactional (use git, or re-run, to undo).
func applyChanges(changes []initdriver.Change) error {
	type undo struct {
		path       string
		backup     string // backup written for a Modify (empty for Create)
		wasCreated bool
		madeDirs   []string // directories this change created, deepest first
	}
	var done []undo

	rollback := func() {
		for i := len(done) - 1; i >= 0; i-- {
			u := done[i]
			if u.wasCreated {
				_ = os.Remove(u.path)
			} else if u.backup != "" {
				// The backup carries the original content and mode.
				_ = os.Rename(u.backup, u.path)
			}
			// Best-effort: os.Remove refuses non-empty dirs, which is what we
			// want — only directories we created (now empty again) go away.
			for _, d := range u.madeDirs {
				_ = os.Remove(d)
			}
		}
	}

	for _, c := range changes {
		madeDirs, err := mkdirAllTracked(filepath.Dir(c.AbsPath))
		if err != nil {
			rollback()
			return err
		}

		u := undo{path: c.AbsPath, wasCreated: c.Kind == initdriver.Create, madeDirs: madeDirs}
		if c.Kind == initdriver.Modify {
			u.backup, err = freeBackupPath(c.AbsPath)
			if err != nil {
				rollback()
				return err
			}
			if err := copyFile(c.AbsPath, u.backup); err != nil {
				rollback()
				return err
			}
		}
		// Record the undo before writing: WriteFile truncates on open, so a
		// write that fails partway must still restore this file's backup (or
		// remove the partial create), not just the earlier changes'.
		done = append(done, u)
		if err := os.WriteFile(c.AbsPath, c.New, 0o644); err != nil {
			rollback()
			return fmt.Errorf("writing %s: %w", c.RelPath, err)
		}
	}

	// All writes succeeded — drop the transient backups.
	for _, u := range done {
		if u.backup != "" {
			_ = os.Remove(u.backup)
		}
	}
	return nil
}

// mkdirAllTracked creates dir like os.MkdirAll and returns the directories it
// actually created, deepest first, so a rollback can remove them again.
func mkdirAllTracked(dir string) ([]string, error) {
	var created []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		created = append(created, d)
		if parent := filepath.Dir(d); parent == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return created, nil
}

// freeBackupPath returns <path>.bak, or <path>.bak2/.bak3/… when a file (the
// user's own) already sits at the earlier names, so a backup never clobbers it.
func freeBackupPath(path string) (string, error) {
	name := path + ".bak"
	for i := 2; ; i++ {
		if _, err := os.Lstat(name); errors.Is(err, fs.ErrNotExist) {
			return name, nil
		}
		if i > 99 {
			return "", fmt.Errorf("no free backup name for %s", path)
		}
		name = fmt.Sprintf("%s.bak%d", path, i)
	}
}

// copyFile writes src's content to dst, preserving src's file mode so a
// rollback rename restores the original permissions too.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dst, data, info.Mode().Perm()); err != nil {
		return err
	}
	// WriteFile's perm is masked by umask on create; make the mode exact.
	return os.Chmod(dst, info.Mode().Perm())
}

func printCelebration(out io.Writer, buildTargets bool) {
	fmt.Fprintf(out, "\n%s\n", ui.Celebrate("Driverforge is set up in your driver!"))
	fmt.Fprintln(out, "\nNext steps:")
	if buildTargets {
		fmt.Fprintln(out, "  • Edit per-build config in src/config.lua (default) and src/config.release.lua.")
		fmt.Fprintln(out, "  • driverforge build   then   driverforge deploy   (release: driverforge build --configuration release)")
	} else {
		fmt.Fprintln(out, "  • driverforge build   then   driverforge deploy")
	}
	fmt.Fprintln(out, "  • Review the wrapped OnDriverInit in your driver entry.")
}
