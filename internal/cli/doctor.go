package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/internal/ui"
)

// newDoctorCmd checks that a project still carries everything `driverforge init`
// set up, and repairs what drifted.
//
// It exists because init is deliberately once-only and `upgrade` only swaps the
// vendored bundle, which left a gap: a project could hold a current SDK version
// in .driverforge/config.json with no bundle in the tree and no wiring in the
// entry Lua, and nothing would either notice or fix it.
//
// The repair is Plan — the same transforms init uses. Every one of them is
// idempotent, so planning against a healthy project yields nothing and planning
// against a drifted one yields exactly the missing pieces. There is no separate
// repair logic to keep in step with init.
func newDoctorCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check a driver's Driverforge setup and repair what's missing",
		Long: "Check that this driver still has everything `driverforge init` set up —\n" +
			"the SDK wiring in the entry Lua, the embedded bundle, the manifest's\n" +
			"squishLua and vendor item, and the squishy module declarations — and\n" +
			"offer to restore whatever is missing.\n\n" +
			"Diagnosis is local and needs no network. Only a repair that has to\n" +
			"rewrite the entry Lua looks up the project's driver token.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), cmd.OutOrStdout(), yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "apply the repairs without confirming")
	return cmd
}

func runDoctor(ctx context.Context, out io.Writer, yes bool) error {
	proj, err := project.Locate(flagProjectDir)
	if err != nil {
		return err
	}
	cfg, err := driverforgeconfig.Load(proj.Dir)
	if err != nil {
		return err
	}
	if cfg == nil {
		// Nothing to diagnose: this is init's job, not a repair.
		return clierr.UserT("Not set up for Driverforge",
			fmt.Sprintf("%s has no .driverforge/config.json", proj.Dir),
			"run `driverforge init` to set this driver up")
	}

	buildTargets := len(cfg.BuildTargets) > 0
	diag := initdriver.Diagnose(proj, buildTargets)
	printDiagnosis(out, diag)
	if diag.Healthy() {
		return nil
	}

	changes, warnings, err := planRepair(ctx, out, proj, cfg, diag)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		// Diagnose found drift the transforms decline to fix. Say so rather than
		// reporting a silent success the checks plainly contradict.
		return clierr.UserT("Nothing to repair automatically",
			"the checks above failed, but no file change would fix them",
			"inspect the reported files, or re-create the project with `driverforge init`")
	}

	if !yes {
		ok, cerr := reviewCodeChanges(ctx, out, warnings, changes)
		if cerr != nil {
			return cerr
		}
		if !ok {
			return clierr.Silent()
		}
	}
	if err := executeFileChanges(ctx, out, proj, "", changes); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n%s\n", ui.Celebrate("Repaired — this driver is set up again."))
	return nil
}

// printDiagnosis reports each check, so a healthy run says what it verified
// rather than printing nothing.
func printDiagnosis(out io.Writer, d initdriver.Diagnosis) {
	fmt.Fprintf(out, "\n%s\n", ui.Heading("Checking this driver's Driverforge setup:"))
	for _, c := range d.Checks {
		if c.OK {
			fmt.Fprintf(out, "  %s %s\n", ui.StatusOK("✓"), ui.StatusMuted(c.What))
		} else {
			fmt.Fprintf(out, "  %s %s\n", ui.StatusWarn("✗"), c.What)
		}
	}
	if d.Healthy() {
		fmt.Fprintf(out, "\n%s\n", ui.Success("Everything init sets up is in place."))
	}
}

// planRepair rebuilds the Decision init made and re-runs Plan. Everything but
// the project token comes from .driverforge/config.json; the token is fetched
// only when the entry Lua actually needs rewriting, so a bundle-only repair
// stays offline.
func planRepair(ctx context.Context, out io.Writer, proj project.Project, cfg *driverforgeconfig.Config, diag initdriver.Diagnosis) ([]initdriver.Change, []string, error) {
	bundle, version, err := repairBundle(ctx, out, cfg.SDKVersion)
	if err != nil {
		return nil, nil, err
	}

	var token string
	if diag.EntryPatchNeeded {
		token, err = lookupProjectToken(ctx, out, cfg)
		if err != nil {
			return nil, nil, err
		}
	}

	res, err := initdriver.Plan(proj, initdriver.Decision{
		SDKBundle:    bundle,
		SDKVersion:   version,
		ProjectToken: token,
		BuildTargets: len(cfg.BuildTargets) > 0,
		Targets:      cfg.BuildTargets,
		LoggerGlobal: cfg.Logger,
	})
	if err != nil {
		return nil, nil, err
	}
	return res.Changes, res.Warnings, nil
}

// repairBundle returns the SDK bytes to embed: the version the config records,
// so a repair restores the project to what it claimed to be rather than
// silently upgrading it. Bumping the SDK stays `driverforge upgrade`'s job.
func repairBundle(ctx context.Context, out io.Writer, version string) ([]byte, string, error) {
	if version == "" {
		return nil, "", clierr.UserT("No SDK version recorded",
			"the project config doesn't say which SDK version it uses",
			"run `driverforge upgrade` to embed the current SDK")
	}
	path, v, err := sdkrelease.EnsureCached(ctx, version)
	if err != nil {
		return nil, "", err
	}
	bundle, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	fmt.Fprintf(out, "\n%s\n", ui.Success("Using Driverforge SDK "+v))
	return bundle, v, nil
}

// lookupProjectToken recovers the drv_ token for the configured project.
// .driverforge/config.json deliberately holds no secrets and the token normally
// lives inlined in the entry Lua — which, when the entry needs patching, is
// exactly where it isn't. So it comes from the graph.
func lookupProjectToken(ctx context.Context, out io.Writer, cfg *driverforgeconfig.Config) (string, error) {
	if cfg.Project.Slug == "" {
		return "", clierr.UserT("Project not recorded",
			"the project config doesn't name a Driverforge project",
			"re-create the setup with `driverforge init`")
	}
	var token string
	err := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI),
		fmt.Sprintf("Looking up project %s…", cfg.Project.Slug),
		func() (string, error) {
			projects, e := driverforgeapi.New().ListAllProjects(ctx)
			if e != nil {
				return "", e
			}
			for _, p := range projects {
				if p.Slug == cfg.Project.Slug {
					token = p.Token
					return "Found project " + firstNonEmpty(p.Name, p.Slug), nil
				}
			}
			return "", clierr.UserT("Project not found",
				fmt.Sprintf("no project %q in your organizations", cfg.Project.Slug),
				"check `driverforge project list`, or re-run `driverforge init`")
		})
	if err != nil {
		return "", err
	}
	if token == "" {
		return "", clierr.UserT("Project has no driver token",
			fmt.Sprintf("project %q returned no drv_ token", cfg.Project.Slug),
			"re-run `driverforge init`, or contact support")
	}
	return token, nil
}
