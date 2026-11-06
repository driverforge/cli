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
// `driverforge init` impossible or pointless: no project, an
// already-initialised project, not being logged in, or a dirty tree (which a
// dry run, writing nothing, doesn't mind).
//
// A logged-out user is a hard stop here rather than mid-flow; a missing org, by
// contrast, is resolvable in-flow (see resolveOrg) and is deliberately not checked.
//
// cfg is non-nil when the project is already initialised: init has reported
// the setup and stops without error.
func initPreflight(ctx context.Context, out io.Writer, projectDir string, o initOpts) (proj project.Project, cfg *driverforgeconfig.Config, err error) {
	proj, err = requireProject(projectDir)
	if err != nil {
		return project.Project{}, nil, err
	}

	// Already initialised? The committed .driverforge/config.json is the authoritative
	// marker — report the setup rather than dragging the user through it again.
	cfg, err = driverforgeconfig.Load(proj.Dir)
	if err != nil {
		return project.Project{}, nil, err
	}
	if cfg != nil {
		return proj, cfg, handleAlreadyInit(out, proj, cfg, o.versioning)
	}

	if err := requireLogin(ctx); err != nil {
		return project.Project{}, nil, err
	}
	if !o.dryRun {
		if err := requireCleanTree(ctx, proj.Dir, "driverforge init"); err != nil {
			return project.Project{}, nil, err
		}
	}
	return proj, nil, nil
}

// gatherOrgAndProject resolves which Driverforge project the driver links to,
// without creating anything. --project names it up front (the org comes from the
// slug, or --org); --create-project names a new one, in --org or the active
// org; otherwise the org is resolved — showing the picker when none is active —
// and the project chosen or described for later creation.
func gatherOrgAndProject(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, o initOpts) (orgctx.Org, projectPlan, error) {
	if o.project != "" {
		ref := o.project
		if o.org != "" {
			ref = o.org + "/" + ref
		}
		return linkProjectByRef(ctx, out, cl, ref)
	}

	var (
		org orgctx.Org
		err error
	)
	if o.org != "" {
		org, err = orgBySlug(ctx, cl, o.org)
		if err == nil {
			err = useOrg(org)
		}
	} else {
		org, err = resolveOrg(ctx, cl)
	}
	if err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	fmt.Fprintln(out, ui.Success("Organization: "+orgLabel(org.Name, org.Slug)))

	if o.createProject != "" {
		platforms, err := loadPlatforms(ctx, out, cl)
		if err != nil {
			return orgctx.Org{}, projectPlan{}, err
		}
		platform, err := choosePlatform(platforms, o.platform)
		if err != nil {
			return orgctx.Org{}, projectPlan{}, err
		}
		fmt.Fprintln(out, ui.Success("Platform: "+platform.Name))
		fmt.Fprintln(out, ui.Success("Project: "+o.createProject+" (new)"))
		return org, projectPlan{create: &createIntent{name: o.createProject, description: o.description, platform: platform}}, nil
	}

	pp, err := gatherProject(ctx, out, cl, org)
	if err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	return org, pp, nil
}

// gatherDriverShape collects the remaining decisions about what init will write —
// build configurations, log forwarding, and the versioning scheme — prompting for
// each. Nothing is created or written here.
func gatherDriverShape(out io.Writer, proj project.Project, bundle []byte, version, entryRel string, o initOpts) (driverPlan, error) {
	dp := driverPlan{bundle: bundle, version: version, entryRel: entryRel}

	// Build targets are opt-in; plain drivers carry no ENV system.
	buildTargets, err := o.buildConfigs, error(nil)
	if !o.buildConfigs && !o.noBuildConfigs {
		buildTargets, err = ui.Confirm("Set up build configurations (default/release)?",
			fmt.Sprintf("Adds config.lua (your default, used by `driverforge build`) and config.release.lua for release builds (`driverforge build --configuration release`) — so each build can point at different endpoints (e.g. a local/dev device vs production). Skip if every build uses the same config — see %s", buildConfigDocsURL), false)
	}
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
	switch {
	case o.logger != "":
		dp.logger = o.logger
	case !o.noLogger:
		dp.logger, err = resolveLoggerGlobal(proj.SrcDir, entryRel)
		if err != nil {
			return driverPlan{}, err
		}
	}
	if dp.logger != "" {
		fmt.Fprintln(out, ui.Success("Log forwarding: "+dp.logger))
	} else {
		fmt.Fprintln(out, ui.Skipped("Log forwarding: off"))
	}

	// How driverforge bumps <version> (build --increment, every deploy). Detected from
	// the authored value and confirmed — never silently converted.
	dp.scheme, err = gatherVersioningScheme(out, proj, o.versioning)
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
// wraps OnDriverInit, links or creates an Driverforge project, and (optionally)
// scaffolds build targets — showing a diff for approval before writing anything.
// Every question has a flag, so an agent or a script can run it too.
func newInitCmd() *cobra.Command {
	var o initOpts
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up Driverforge in an existing driver project",
		Long: "Set up Driverforge in an existing driver project: embed the SDK, wrap\n" +
			"OnDriverInit so events, errors and logs are captured, link or create the\n" +
			"Driverforge project the driver reports to, and optionally add build\n" +
			"configurations.\n\n" +
			"In a terminal, init asks each question and shows the changes for approval;\n" +
			"a flag answers its question instead. Without a terminal (a coding agent,\n" +
			"CI, or --no-tui) every question needs its flag: preview the plan and its\n" +
			"diffs with --dry-run, then apply it with --yes.",
		Example: "  driverforge init\n" +
			"  driverforge init --create-project \"Pool Pump\" --versioning date --no-build-configs \\\n" +
			"      --logger Log --branch driverforge-init --dry-run\n" +
			"  driverforge init --project acme/pool-pump --versioning date --no-build-configs \\\n" +
			"      --no-logger --no-branch --yes",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.commitMsgSet = cmd.Flags().Changed("commit-message")
			doc, err := runInit(cmd.Context(), textOut(cmd), flagProjectDir, o)
			if err != nil {
				return err
			}
			return emit(cmd, doc, noText)
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.project, "project", "", "link an existing project: <org-slug>/<project-slug>, or a project slug")
	f.StringVar(&o.createProject, "create-project", "", "create a project with this name for the driver to report to")
	f.StringVar(&o.org, "org", "", "the organization to create the project in (default: the active one), or to qualify a --project slug")
	f.StringVar(&o.platform, "platform", "", "the new project's platform, by id or name (default: the only supported one)")
	f.StringVar(&o.description, "description", "", "the new project's description")
	f.StringVar(&o.versioning, "versioning", "", "how deploys bump <version>: date (YYYYMMDDNN) or integer")
	f.BoolVar(&o.buildConfigs, "build-configs", false, "add config.lua and config.release.lua build configurations")
	f.BoolVar(&o.noBuildConfigs, "no-build-configs", false, "don't add build configurations")
	f.StringVar(&o.logger, "logger", "", "forward logs from this logger global (e.g. Log)")
	f.BoolVar(&o.noLogger, "no-logger", false, "don't forward the driver's logs")
	f.StringVar(&o.branch, "branch", "", "create this git branch and commit the changes to it")
	f.BoolVar(&o.noBranch, "no-branch", false, "write the changes to the current branch, uncommitted")
	f.StringVar(&o.commitMsg, "commit-message", "", "the commit message on --branch (default \""+defaultInitCommitMessage+"\"; empty leaves the changes uncommitted)")
	f.BoolVarP(&o.yes, "yes", "y", false, "apply without reviewing the changes")
	f.BoolVar(&o.dryRun, "dry-run", false, "report the plan and its diffs without creating or writing anything")
	return cmd
}

// runInit sets a project up exactly once: it always embeds the newest Anvil
// SDK (cached copy first). Updating the SDK afterwards is the `driverforge
// upgrade` picker — init never mutates an already-initialised project.
func runInit(ctx context.Context, out io.Writer, projectDir string, o initOpts) (initOut, error) {
	doc := newInitOut()
	doc.DryRun = o.dryRun
	if err := o.validate(); err != nil {
		return doc, err
	}
	proj, cfg, err := initPreflight(ctx, out, projectDir, o)
	if err != nil {
		return doc, err
	}
	if cfg != nil {
		return alreadySetUpOut(doc, cfg), nil
	}

	cl := driverforgeapi.New()

	// --- Gather: collect every decision without changing anything ---
	g, err := gatherInit(ctx, out, cl, proj, o)
	if err != nil {
		return doc, err
	}
	doc.Org = initOrgOut{Slug: g.org.Slug, Name: g.org.Name}
	doc.setProject(g.pp, nil)
	doc.SDKVersion, doc.Versioning, doc.Logger = g.dp.version, g.dp.scheme, g.dp.logger
	doc.BuildConfigs = g.dp.buildTargets
	doc.Branch, doc.CommitMessage = g.branch, g.commitMsg

	// --- Build the change set for review (placeholder key/project; nothing is
	// created until the user confirms) ---
	reviewPlan, err := buildInitChanges(proj, g.dp, g.org, g.pp.reviewProject(), g.pp.reviewToken())
	if err != nil {
		return doc, err
	}
	doc.setChanges(reviewPlan.Changes)
	for _, w := range reviewPlan.Warnings {
		doc.Warnings = append(doc.Warnings, warning{Code: codeWarning, Message: w})
	}
	if reviewPlan.Empty() {
		fmt.Fprintln(out, "\nThis project is already set up for Driverforge — nothing to change.")
		return doc, nil
	}

	if apply, err := approveInit(ctx, out, o, g, reviewPlan); err != nil || !apply {
		return doc, err
	}

	// --- Execute the confirmed plan with a live progress list ---
	linked, written, err := executeInit(ctx, out, proj, cl, g.org, g.pp, g.dp, g.branch, g.commitMsg)
	if err != nil {
		return doc, err
	}
	doc.Applied = true
	doc.setProject(g.pp, &linked)
	doc.setChanges(written)

	printCelebration(out, g.dp.buildTargets)
	return doc, nil
}

// alreadySetUpOut reports a project init has already set up.
func alreadySetUpOut(doc initOut, cfg *driverforgeconfig.Config) initOut {
	doc.AlreadySetUp = true
	doc.Org = initOrgOut{Slug: cfg.Org}
	doc.Project = initProjectOut{Action: "link", Slug: cfg.Project.Slug, Name: cfg.Project.Name}
	doc.SDKVersion, doc.Versioning, doc.Logger = cfg.SDKVersion, cfg.Versioning, cfg.Logger
	doc.BuildConfigs = len(cfg.BuildTargets) > 0
	return doc
}

// initDecisions is everything init gathered before building the change set.
type initDecisions struct {
	org       orgctx.Org
	pp        projectPlan
	dp        driverPlan
	branch    string
	commitMsg string
}

// gatherInit collects every decision, from flags or by asking. Without a
// terminal, a missing flag fails here before anything touches the network.
func gatherInit(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, proj project.Project, o initOpts) (initDecisions, error) {
	var g initDecisions
	entryRel, err := initdriver.EntryFile(proj.SrcDir)
	if err != nil {
		return g, err
	}
	if !ui.Interactive(flagNoTUI) {
		if missing, detected := o.missingAnswers(ctx, proj, entryRel); len(missing) > 0 {
			return g, inputRequired(missing, detected)
		}
	} else {
		fmt.Fprintf(out, "\n%s", ui.InitIntro())
	}

	// Resolve and cache the SDK bundle to embed (newest cached, else latest).
	bundle, version, err := loadSDK(ctx, out)
	if err != nil {
		return g, err
	}
	if g.org, g.pp, err = gatherOrgAndProject(ctx, out, cl, o); err != nil {
		return g, err
	}
	if g.dp, err = gatherDriverShape(out, proj, bundle, version, entryRel, o); err != nil {
		return g, err
	}
	g.branch, g.commitMsg, err = initBranchIntent(ctx, out, proj.Dir, o)
	return g, err
}

// approveInit decides whether to apply the plan: a dry run prints it and
// stops, --yes applies it, and otherwise the user reviews the code changes and
// confirms the whole plan. Nothing has been written when it returns.
func approveInit(ctx context.Context, out io.Writer, o initOpts, g initDecisions, plan *initdriver.Result) (bool, error) {
	// The SDK bundle is excluded from review — it's a vendored blob, visible
	// in git; the review is about what we change in the user's own files.
	reviewable, sdkEmbedded := reviewableChanges(plan.Changes)
	summary := func() {
		printPlan(out, g.pp, g.dp.buildTargets, g.branch, g.commitMsg, len(reviewable), sdkEmbedded)
	}
	switch {
	case o.dryRun:
		printDryRun(out, plan.Warnings, reviewable)
		summary()
		fmt.Fprintln(out, "\nDry run: nothing was created or written. Re-run with --yes to apply.")
		return false, nil
	case o.yes:
		return true, nil
	}

	accepted, err := reviewCodeChanges(ctx, out, plan.Warnings, reviewable)
	if err != nil {
		return false, err
	}
	if accepted {
		fmt.Fprintln(out, ui.Success(fmt.Sprintf("Code changes: %d file%s", len(reviewable), plural(len(reviewable)))))
		// --- Final confirmation of the whole plan (still nothing written) ---
		summary()
		accepted, err = ui.Confirm("Apply this plan?", "Nothing has changed yet — this links/creates the project and writes the files.", true)
		if err != nil {
			return false, err
		}
	}
	if !accepted {
		fmt.Fprintln(out, "\nNo changes made.")
		return false, clierr.Silent()
	}
	return true, nil
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
