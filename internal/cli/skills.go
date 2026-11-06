package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/gitrepo"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/skills"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Install the agent skills that teach a coding agent to use driverforge",
		Long: `Agent skills teach a coding agent how to work with driverforge: the driver
development cycle of deploying, triggering and reading the driver's telemetry.
They ship inside the CLI, so an installed skill describes the CLI that installed
it. Run ` + "`driverforge skills update`" + ` after upgrading.`,
	}
	cmd.AddCommand(newSkillsListCmd(), newSkillsInstallCmd(), newSkillsUpdateCmd())
	return cmd
}

type skillOut struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

type skillsListOut struct {
	CLIVersion string     `json:"cli_version"`
	Skills     []skillOut `json:"skills"`
}

func newSkillsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the skills this CLI ships",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			list, err := skills.List()
			if err != nil {
				return err
			}
			doc := skillsListOut{CLIVersion: buildinfo.Version, Skills: []skillOut{}}
			for _, s := range list {
				doc.Skills = append(doc.Skills, skillOut(s))
			}
			return emit(cmd, doc, func(w io.Writer) error {
				tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tDESCRIPTION")
				for _, s := range list {
					desc, _, _ := strings.Cut(s.Description, ". ")
					fmt.Fprintf(tw, "%s\t%s\n", s.Name, strings.TrimSuffix(desc, "."))
				}
				return tw.Flush()
			})
		},
	}
}

type skillInstallOut struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Action string `json:"action"`
	Files  int    `json:"files"`
}

type skillsInstallOut struct {
	CLIVersion string            `json:"cli_version"`
	Dir        string            `json:"dir"`
	Skills     []skillInstallOut `json:"skills"`
}

func newSkillsInstallCmd() *cobra.Command {
	var (
		global bool
		path   string
		force  bool
	)
	cmd := &cobra.Command{
		Use:   "install [skill...]",
		Short: "Install skills into a coding agent's skills directory",
		Long: `Install skills (all of them by default) into .agents/skills at the root of
the current git repository, the skills directory coding agents read. Commit it,
and everyone working on the driver gets the same skills. --global installs into
~/.agents/skills for every project instead; --path installs anywhere.

Installing again replaces the copy a previous install wrote. A directory of
the same name that driverforge didn't write is left alone unless --force.`,
		Example: `  driverforge skills install
  driverforge skills install --global
  driverforge skills install --path ./tools/skills`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsInstall(cmd, args, global, path, force)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "install for the current user, into ~/.agents/skills")
	cmd.Flags().StringVar(&path, "path", "", "install into this skills directory")
	cmd.Flags().BoolVar(&force, "force", false, "replace a directory driverforge didn't install")
	return cmd
}

func runSkillsInstall(cmd *cobra.Command, names []string, global bool, path string, force bool) error {
	root, err := skillsDir(cmd.Context(), global, path)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		list, err := skills.List()
		if err != nil {
			return err
		}
		for _, s := range list {
			names = append(names, s.Name)
		}
	}

	out := textOut(cmd)
	doc := skillsInstallOut{CLIVersion: buildinfo.Version, Dir: root, Skills: []skillInstallOut{}}
	for _, name := range names {
		res, err := skills.Install(root, name, buildinfo.Version, force)
		switch {
		case errors.Is(err, skills.ErrUnknown):
			return clierr.WithCode(clierr.User(
				fmt.Sprintf("driverforge doesn't ship a skill called %q", name),
				"Run `driverforge skills list` for the skills it ships."), codeSkillNotFound)
		case errors.Is(err, skills.ErrNotOwned):
			return clierr.WithCode(clierr.User(
				res.Path+" already exists and wasn't installed by driverforge",
				"Move it aside, or pass --force to replace it."), codeSkillConflict)
		case err != nil:
			return err
		}
		doc.Skills = append(doc.Skills, skillInstallOut{Name: res.Name, Path: res.Path, Action: string(res.Action), Files: res.Files})
		if res.Action == skills.Unchanged {
			fmt.Fprintf(out, "– %s is up to date in %s\n", res.Name, res.Path)
		} else {
			fmt.Fprintf(out, "✓ %s %s to %s\n", capitalise(string(res.Action)), res.Name, res.Path)
		}
	}
	return emit(cmd, doc, noText)
}

// skillsDir is the directory skills install into: --path, ~/.agents/skills
// with --global, otherwise .agents/skills at the root of the git repository
// (or, outside one, the driver project).
func skillsDir(ctx context.Context, global bool, path string) (string, error) {
	switch {
	case path != "" && global:
		return "", clierr.WithCode(clierr.User("--global and --path can't be used together", "Pass one of them."), codeUsage)
	case path != "":
		return filepath.Abs(expandHome(path))
	case global:
		return globalSkillsDir()
	}
	return repoSkillsDir(ctx)
}

// globalSkillsDir is ~/.agents/skills.
func globalSkillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", clierr.Wrap(err, "your home directory couldn't be found", "Pass --path with the skills directory to use.")
	}
	return filepath.Join(home, ".agents", "skills"), nil
}

// repoSkillsDir is .agents/skills at the root of the git repository, or of the
// driver project outside one.
func repoSkillsDir(ctx context.Context) (string, error) {
	if root, ok := gitrepo.Root(ctx, flagProjectDir); ok {
		return filepath.Join(root, ".agents", "skills"), nil
	}
	p, err := project.Locate(flagProjectDir)
	if err != nil {
		return "", clierr.WithCode(clierr.Wrap(err,
			"this isn't a git repository or a driver project",
			"Run from the driver's repository (or pass --project-dir), or install for every project with --global."), codeNotAProject)
	}
	return filepath.Join(p.Dir, ".agents", "skills"), nil
}

// expandHome expands a leading ~ the shell left alone (a quoted path, or one
// passed as --dir=~/…).
func expandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}

type skillsUpdateOut struct {
	CLIVersion string            `json:"cli_version"`
	Skills     []skillInstallOut `json:"skills"`
}

func newSkillsUpdateCmd() *cobra.Command {
	var (
		global bool
		path   string
	)
	cmd := &cobra.Command{
		Use:   "update [skill...]",
		Short: "Update installed skills to the versions this CLI ships",
		Long: `Update the skills driverforge installed (all of them by default) to the
versions this CLI ships. With no flag it updates them in both standard
locations: .agents/skills at the root of the current git repository, and
~/.agents/skills. --global updates only ~/.agents/skills, and --path only the
given directory. Run it after upgrading driverforge.`,
		Example: `  driverforge skills update
  driverforge skills update --global
  driverforge skills update --path ./tools/skills`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsUpdate(cmd, args, global, path)
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "update only ~/.agents/skills")
	cmd.Flags().StringVar(&path, "path", "", "update only this skills directory")
	return cmd
}

func runSkillsUpdate(cmd *cobra.Command, names []string, global bool, path string) error {
	roots, err := updateRoots(cmd.Context(), global, path)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	for _, n := range names {
		if _, err := skills.Files(n, buildinfo.Version); errors.Is(err, skills.ErrUnknown) {
			return clierr.WithCode(clierr.User(
				fmt.Sprintf("driverforge doesn't ship a skill called %q", n),
				"Run `driverforge skills list` for the skills it ships."), codeSkillNotFound)
		}
		want[n] = true
	}

	out := textOut(cmd)
	doc := skillsUpdateOut{CLIVersion: buildinfo.Version, Skills: []skillInstallOut{}}
	found := map[string]bool{}
	for _, root := range roots {
		installed, err := skills.InstalledIn(root)
		if err != nil {
			return err
		}
		for _, name := range installed {
			if len(want) > 0 && !want[name] {
				continue
			}
			res, err := skills.Install(root, name, buildinfo.Version, false)
			if errors.Is(err, skills.ErrUnknown) {
				fmt.Fprintf(out, "⚠ %s in %s isn't shipped by this CLI any more; remove it by hand\n", name, root)
				continue
			}
			if err != nil {
				return err
			}
			found[name] = true
			doc.Skills = append(doc.Skills, skillInstallOut{Name: res.Name, Path: res.Path, Action: string(res.Action), Files: res.Files})
			if res.Action == skills.Unchanged {
				fmt.Fprintf(out, "– %s is up to date in %s\n", res.Name, res.Path)
			} else {
				fmt.Fprintf(out, "✓ Updated %s in %s\n", res.Name, res.Path)
			}
		}
	}

	var missing []string
	for _, n := range names {
		if !found[n] {
			missing = append(missing, n)
		}
	}
	if len(doc.Skills) == 0 || len(missing) > 0 {
		what := "No skills installed by driverforge"
		if len(missing) > 0 {
			what = strings.Join(missing, ", ") + " isn't installed"
		}
		return newCmdErr(codeSkillNotInstalled, what+" in "+strings.Join(roots, " or ")+".",
			"Install with `driverforge skills install` (add --global for every project).").
			with("searched", roots).asUserError()
	}
	return emit(cmd, doc, noText)
}

// updateRoots are the skills directories update looks in: --path, or
// ~/.agents/skills with --global, otherwise the repository's and the user's.
func updateRoots(ctx context.Context, global bool, path string) ([]string, error) {
	if path != "" || global {
		root, err := skillsDir(ctx, global, path)
		if err != nil {
			return nil, err
		}
		return []string{root}, nil
	}
	var roots []string
	if repo, err := repoSkillsDir(ctx); err == nil {
		roots = append(roots, repo)
	}
	home, err := globalSkillsDir()
	if err != nil {
		return nil, err
	}
	if len(roots) == 0 || roots[0] != home {
		roots = append(roots, home)
	}
	return roots, nil
}
