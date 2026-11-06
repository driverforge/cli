package cli

import (
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
it. Re-run ` + "`driverforge skills install`" + ` after upgrading.`,
	}
	cmd.AddCommand(newSkillsListCmd(), newSkillsInstallCmd())
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
		inProject bool
		dir       string
		force     bool
	)
	cmd := &cobra.Command{
		Use:   "install [skill...]",
		Short: "Install skills into a coding agent's skills directory",
		Long: `Install skills (all of them by default) into ~/.agents/skills, the shared
skills directory coding agents read. --project installs into the driver repo's
.agents/skills instead, so everyone working on the driver gets them; --dir
installs anywhere.

Installing again replaces the copy a previous install wrote. A directory of
the same name that driverforge didn't write is left alone unless --force.`,
		Example: `  driverforge skills install
  driverforge skills install --project
  driverforge skills install --dir ./tools/skills`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSkillsInstall(cmd, args, inProject, dir, force)
		},
	}
	cmd.Flags().BoolVar(&inProject, "project", false, "install into the driver repo's .agents/skills")
	cmd.Flags().StringVar(&dir, "dir", "", "install into this skills directory")
	cmd.Flags().BoolVar(&force, "force", false, "replace a directory driverforge didn't install")
	return cmd
}

func runSkillsInstall(cmd *cobra.Command, names []string, inProject bool, dir string, force bool) error {
	root, err := skillsDir(inProject, dir)
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

// skillsDir is the directory skills install into: --dir, the driver repo's
// .agents/skills with --project, otherwise ~/.agents/skills.
func skillsDir(inProject bool, dir string) (string, error) {
	switch {
	case dir != "" && inProject:
		return "", clierr.WithCode(clierr.User("--project and --dir can't be used together", "Pass one of them."), codeUsage)
	case dir != "":
		return filepath.Abs(expandHome(dir))
	case inProject:
		p, err := project.Locate(flagProjectDir)
		if err != nil {
			return "", clierr.WithCode(clierr.Wrap(err,
				"this isn't a driver repo",
				"Run from the driver's directory (or pass --project-dir), or pass --dir."), codeNotAProject)
		}
		return filepath.Join(p.Dir, ".agents", "skills"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", clierr.Wrap(err, "your home directory couldn't be found", "Pass --dir with the skills directory to install into.")
	}
	return filepath.Join(home, ".agents", "skills"), nil
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
