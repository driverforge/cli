package cli

import (
	"context"
	"fmt"
	"io"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/telemetry"
	"github.com/driverforge/cli/internal/ui"
)

// dashboardURL is where users manage projects, API keys, and their plan.
const dashboardURL = "https://go.driverforge.com/app"

// driverPlan is the shape init will give the driver: the SDK bundle to embed, how
// its version is bumped, whether build configurations are scaffolded, and the
// logger/entry wiring. These are gathered together, reviewed together and written
// together, so they travel as one value rather than as a parameter list.
type driverPlan struct {
	bundle       []byte
	version      string
	buildTargets bool
	targets      []string
	logger       string
	entryRel     string
	scheme       string
}

// initPreflight rejects, before any prompting, the conditions that make
// `driverforge init` impossible or pointless: a non-interactive terminal, no
// project, an already-initialised project, not being logged in, or a dirty tree.
//
// A logged-out user is a hard stop here rather than mid-flow; a missing org, by
// contrast, is resolvable in-flow (see resolveOrg) and is deliberately not checked.
//
// done is true when init has already reported the situation and should stop
// without error — today only the already-initialised case.
func initPreflight(ctx context.Context, out io.Writer, projectDir string) (proj project.Project, done bool, err error) {
	if !ui.Interactive(flagNoTUI) {
		return project.Project{}, false, &clierr.UserError{
			Title:   "Interactive terminal required",
			Message: "driverforge init walks you through linking a project and approving changes",
			Hint:    "run it in an interactive terminal",
		}
	}

	proj, err = requireProject(projectDir)
	if err != nil {
		return project.Project{}, false, err
	}

	// Already initialised? The committed .driverforge/config.json is the authoritative
	// marker — report the setup rather than dragging the user through it again.
	cfg, err := driverforgeconfig.Load(proj.Dir)
	if err != nil {
		return project.Project{}, false, err
	}
	if cfg != nil {
		return proj, true, handleAlreadyInit(out, proj, cfg)
	}

	if err := requireLogin(ctx); err != nil {
		return project.Project{}, false, err
	}
	if err := requireCleanTree(ctx, proj.Dir); err != nil {
		return project.Project{}, false, err
	}
	return proj, false, nil
}

// gatherOrgAndProject resolves which Driverforge project the driver links to,
// without creating anything. --project names it up front (the org comes from the
// slug); otherwise the org is resolved — showing the picker when none is active —
// and the project chosen or described for later creation.
func gatherOrgAndProject(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, projectRef string) (orgctx.Org, projectPlan, error) {
	if projectRef != "" {
		return linkProjectByRef(ctx, out, cl, projectRef)
	}

	org, err := resolveOrg(ctx, cl)
	if err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	fmt.Fprintln(out, ui.Success("Organization: "+orgLabel(org.Name, org.Slug)))

	pp, err := gatherProject(ctx, out, cl, org)
	if err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	return org, pp, nil
}

// gatherDriverShape collects the remaining decisions about what init will write —
// build configurations, log forwarding, and the versioning scheme — prompting for
// each. Nothing is created or written here.
func gatherDriverShape(out io.Writer, proj project.Project, bundle []byte, version, entryRel string) (driverPlan, error) {
	dp := driverPlan{bundle: bundle, version: version, entryRel: entryRel}

	// Build targets are opt-in; plain drivers carry no ENV system.
	buildTargets, err := ui.Confirm("Set up build configurations (default/release)?",
		fmt.Sprintf("Adds config.lua (your default, used by `driverforge build`) and config.release.lua for release builds (`driverforge build --configuration release`) — so each build can point at different endpoints (e.g. a local/dev device vs production). Skip if every build uses the same config — see %s", buildConfigDocsURL), false)
	if err != nil {
		return driverPlan{}, err
	}
	dp.buildTargets = buildTargets
	if buildTargets {
		dp.targets = []string{"debug", "release"}
		fmt.Fprintln(out, ui.Success("Build config: config.lua + config.release.lua"))
	} else {
		fmt.Fprintln(out, ui.Skipped("Build targets: none"))
	}

	// Detect an existing logger to forward; the SDK only forwards logs from a
	// logger the driver passes to Driverforge:Init.
	dp.logger, err = resolveLoggerGlobal(proj.SrcDir, entryRel)
	if err != nil {
		return driverPlan{}, err
	}
	if dp.logger != "" {
		fmt.Fprintln(out, ui.Success("Log forwarding: "+dp.logger))
	} else {
		fmt.Fprintln(out, ui.Skipped("Log forwarding: off"))
	}

	// How driverforge bumps <version> (build --increment, every deploy). Detected from
	// the authored value and confirmed — never silently converted.
	dp.scheme, err = gatherVersioningScheme(out, proj)
	if err != nil {
		return driverPlan{}, err
	}
	return dp, nil
}

// orgLinkURL builds a link to an org-scoped page in the web app.
//
// Release builds route through the shortener — the slug plus ?org=, which the
// app's /a handler turns back into a path — so the page can be re-routed later
// without stranding binaries in the field. Builds with no short-link base (local,
// dev, forks) link straight at the page, the same destination /a would have
// redirected to, so a dev stack needs no /a route. Same wired-but-dark split as
// telemetry.EventsURL.
//
// The org is read here rather than threaded through the error path: both init
// paths persist it with orgctx.SetCurrent before any of these errors can fire
// (resolveOrg, and linkProjectByRef under --project <org>/<project>), so Current()
// is always the org being operated on. Falls back to the bare dashboard if the
// context can't be read at all.
//
// Published contract — see docsBaseURL in usage.go: the slug and its org parameter
// ship in released binaries and can only be re-pointed, never retired.
func orgLinkURL(slug, path string) string {
	org, err := orgctx.Current()
	if err != nil || org.Slug == "" {
		return dashboardURL
	}
	if base := telemetry.ShortLinkBase(); base != "" {
		return base + "/" + slug + "?org=" + url.QueryEscape(org.Slug)
	}
	return telemetry.AppBase() + "/" + org.Slug + "/" + path
}

// newOrgURL starts organization creation. It needs no org slug of its own — it is
// only shown to an account that belongs to none (see resolveOrg) — and the app
// auth-gates it, bouncing a signed-out browser to sign-in with returnTo pointing
// back here. Everyone reaching this hint is already signed in to the CLI
// (requireLogin), so they always have an account to sign in with.
//
// Published contract — see docsBaseURL in usage.go.
const newOrgURL = "https://go.driverforge.com/neworg"

// loggerDocsURL documents the logger interface Driverforge forwards through, so a user
// can check their logger is compatible before wiring it up. Published contract —
// see docsBaseURL in usage.go.
const loggerDocsURL = "https://go.driverforge.com/logfwd"

// buildConfigDocsURL documents the build-configuration model (config.lua +
// config.<configuration>.lua) so a user can decide whether they need it. Published
// contract — see docsBaseURL in usage.go.
const buildConfigDocsURL = "https://go.driverforge.com/buildconfig"

// newInitCmd retrofits an existing driver project for Driverforge: it embeds the SDK,
// wraps OnDriverInit, links an Driverforge project, and (optionally) scaffolds build
// targets — showing a diff for approval before writing anything.
func newInitCmd() *cobra.Command {
	var projectRef string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up Driverforge in an existing driver project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd.Context(), cmd.OutOrStdout(), flagProjectDir, projectRef)
		},
	}
	cmd.Flags().StringVar(&projectRef, "project", "", "link a specific project without prompting: <org-slug>/<project-slug> (or a project slug)")
	return cmd
}

// runInit sets a project up exactly once: it always embeds the newest Anvil
// SDK (cached copy first). Updating the SDK afterwards is the `driverforge
// upgrade` picker — init never mutates an already-initialised project.
func runInit(ctx context.Context, out io.Writer, projectDir, projectRef string) error {
	proj, done, err := initPreflight(ctx, out, projectDir)
	if err != nil || done {
		return err
	}

	fmt.Fprintf(out, "\n%s", ui.InitIntro())

	// Resolve and cache the SDK bundle to embed (newest cached, else latest).
	bundle, version, err := loadSDK(ctx, out)
	if err != nil {
		return err
	}

	entryRel, err := initdriver.EntryFile(proj.SrcDir)
	if err != nil {
		return err
	}

	cl := driverforgeapi.New()

	// --- Gather: collect every decision without changing anything ---
	org, pp, err := gatherOrgAndProject(ctx, out, cl, projectRef)
	if err != nil {
		return err
	}

	dp, err := gatherDriverShape(out, proj, bundle, version, entryRel)
	if err != nil {
		return err
	}

	branch, commitMsg, err := gatherBranchIntent(ctx, out, proj.Dir)
	if err != nil {
		return err
	}

	// --- Build the change set for review (placeholder key/project; nothing is
	// created until the user confirms) ---
	reviewPlan, err := buildInitChanges(proj, dp, org, pp.reviewProject(), pp.reviewToken())
	if err != nil {
		return err
	}
	if reviewPlan.Empty() {
		fmt.Fprintln(out, "\nThis project is already set up for Driverforge — nothing to change.")
		return nil
	}

	// --- Review the code changes (a step in the recipe; still nothing written).
	// The SDK bundle is excluded — it's a vendored blob, visible in git; the
	// review is about what we change in the user's own files. ---
	reviewable, sdkEmbedded := reviewableChanges(reviewPlan.Changes)
	accepted, err := reviewCodeChanges(ctx, out, reviewPlan.Warnings, reviewable)
	if err != nil {
		return err
	}
	if !accepted {
		fmt.Fprintln(out, "\nNo changes made.")
		return clierr.Silent()
	}
	fmt.Fprintln(out, ui.Success(fmt.Sprintf("Code changes: %d file%s", len(reviewable), plural(len(reviewable)))))

	// --- Final confirmation of the whole plan (still nothing written) ---
	ok, err := confirmPlan(out, pp, dp.buildTargets, branch, commitMsg, len(reviewable), sdkEmbedded)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(out, "\nNo changes made.")
		return clierr.Silent()
	}

	// --- Execute the confirmed plan with a live progress list ---
	if err := executeInit(ctx, out, proj, cl, org, pp, dp, branch, commitMsg); err != nil {
		return err
	}

	printCelebration(out, dp.buildTargets)
	return nil
}

// newProjectTokenPlaceholder stands in for a token that does not exist yet: when
// init is creating the project, there is nothing to show until the graph mutation
// runs in the execute phase. Linking an existing project shows its real token,
// because the token is an identifier rather than a secret and the user is
// approving a diff that will put it in their repository.
const newProjectTokenPlaceholder = "<assigned when the project is created>"

// projectPlan is the deferred project decision: link an existing project, or
// create a new one. The graph mutation (and key) happen in the execute phase.
type projectPlan struct {
	existing *driverforgeapi.Project
	create   *createIntent
}

// reviewToken is the token to show in the reviewed diff: the real one when
// linking an existing project, or a placeholder when the project does not exist
// yet.
func (pp projectPlan) reviewToken() string {
	if pp.existing != nil {
		return pp.existing.Token
	}
	return newProjectTokenPlaceholder
}

type createIntent struct {
	name        string
	description string
	platform    driverforgeapi.Platform
}

func (pp projectPlan) displayName() string {
	if pp.existing != nil {
		return pp.existing.Name
	}
	return pp.create.name
}

// reviewProject is the project rendered in the review plan — the real one when
// linking, or a placeholder carrying the entered name when creating.
func (pp projectPlan) reviewProject() driverforgeapi.Project {
	if pp.existing != nil {
		return *pp.existing
	}
	return driverforgeapi.Project{Name: pp.create.name, Slug: "(created on apply)"}
}
