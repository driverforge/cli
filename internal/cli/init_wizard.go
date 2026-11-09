package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/gitrepo"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/versioning"
)

// defaultInitCommitMessage is the suggested commit message for the branch init
// offers to create.
const defaultInitCommitMessage = "chore: add Anvil developer tooling support"

// gatherBranchIntent decides whether (and what) git branch to create and what
// commit message to use for the changes, without touching the repo — that
// happens in the execute phase. defaultBranch and defaultCommitMsg seed the
// prompts (init and the SDK upgrade share the wizard and differ only there).
// Returns ("", "") when not in a repo or the user declines a branch; an empty
// commitMsg (user cleared the prompt) means branch-only, leaving the changes
// uncommitted.
func gatherBranchIntent(ctx context.Context, out io.Writer, projectDir, defaultBranch, defaultCommitMsg string) (branch, commitMsg string, err error) {
	root, ok := gitrepo.Root(ctx, projectDir)
	if !ok {
		return "", "", nil
	}
	// A repo with no commits has an unborn branch — `git checkout -b` would just
	// rename it, not fork history. Skip branch creation until there's a baseline.
	if !gitrepo.HasCommits(ctx, root) {
		fmt.Fprintln(out, ui.Skipped("Git branch: skipped (no commits yet)"))
		return "", "", nil
	}
	desc := "Recommended so you can review and revert these changes easily."
	if cur := gitrepo.CurrentBranch(ctx, root); cur != "" && cur != "HEAD" {
		desc = "You're on " + cur + ". " + desc
	}
	want, err := ui.Confirm("Create a git branch for these changes?", desc, true)
	if err != nil {
		return "", "", err
	}
	if !want {
		fmt.Fprintln(out, ui.Skipped("Git branch: none"))
		return "", "", nil
	}
	name, err := ui.PromptText("Branch name", freeBranchName(ctx, root, defaultBranch))
	if err != nil {
		return "", "", err
	}
	if name == "" {
		fmt.Fprintln(out, ui.Skipped("Git branch: none"))
		return "", "", nil
	}
	if err := checkBranchName(ctx, root, name); err != nil {
		return "", "", err
	}
	fmt.Fprintln(out, ui.Success("Git branch: "+name))

	// The branch exists to hold these changes — commit them onto it so review
	// and revert are one git operation. Clearing the message opts out.
	commitMsg, err = ui.PromptText("Commit message", defaultCommitMsg)
	if err != nil {
		return "", "", err
	}
	if commitMsg == "" {
		fmt.Fprintln(out, ui.Skipped("Commit: none (changes left uncommitted)"))
	} else {
		fmt.Fprintln(out, ui.Success("Commit: "+commitMsg))
	}
	return name, commitMsg, nil
}

// checkBranchName refuses a branch name git won't accept or that's taken.
func checkBranchName(ctx context.Context, root, name string) error {
	if !gitrepo.ValidBranchName(ctx, root, name) {
		return clierr.WithCode(clierr.UserT("Invalid branch name",
			fmt.Sprintf("%q isn't a valid git branch name", name),
			"choose a name without spaces or special characters"), codeUsage)
	}
	if gitrepo.BranchExists(ctx, root, name) {
		return clierr.WithCode(clierr.UserT("Branch already exists",
			fmt.Sprintf("a branch named %q already exists", name),
			"choose a different name"), codeNameTaken)
	}
	return nil
}

// gatherVersioningScheme asks how driverforge should bump the driver's <version>
// (build --increment and every deploy), suggesting the scheme the authored
// value already looks like — an existing numbering is never silently converted.
// A scheme given as --versioning answers it.
func gatherVersioningScheme(out io.Writer, proj project.Project, flag string) (string, error) {
	current, detected := detectScheme(proj)
	if flag != "" {
		fmt.Fprintln(out, ui.Success("Versioning: "+schemeLabel(flag)))
		return flag, nil
	}

	dateNext, _ := versioning.Next(versioning.Date, current, time.Now())
	intLabel := "Incrementing integer — 1, 2, 3, …"
	if next, err := versioning.Next(versioning.Integer, current, time.Now()); err == nil {
		intLabel = fmt.Sprintf("Incrementing integer — %s → %s", current, next)
	}
	desc := "Deploys bump <version> automatically (Director only reloads a higher one); `driverforge build --increment` bumps it by hand."
	if current != "" {
		desc = fmt.Sprintf("driver.xml currently has <version>%s</version>. ", current) + desc
	}
	scheme, err := ui.PickOneDesc("How should driverforge bump this driver's <version>?", desc,
		[]ui.Choice{
			{Label: "Date-based — next: " + dateNext, Value: versioning.Date},
			{Label: intLabel, Value: versioning.Integer},
		}, detected)
	if err != nil {
		return "", err
	}
	fmt.Fprintln(out, ui.Success("Versioning: "+schemeLabel(scheme)))
	return scheme, nil
}

func schemeLabel(scheme string) string {
	if scheme == versioning.Integer {
		return "incrementing integer"
	}
	return "date-based (YYYYMMDDNN)"
}

// requireCleanTree refuses to proceed when the project is in a git repo with a
// dirty work tree, so Driverforge's changes land in isolation (reviewable and easy to
// revert) rather than mixed with the user's uncommitted work — and so there's a
// committed baseline to branch from. Non-git projects are left alone. what
// names the operation in the error, e.g. "driverforge init" or "the SDK
// upgrade" — the same rule guards both, and the message must say which one
// refused.
func requireCleanTree(ctx context.Context, projectDir, what string) error {
	if treeClean(ctx, projectDir) {
		return nil
	}
	return &clierr.UserError{
		Title:   "Git working tree isn't clean",
		Message: what + " changes your driver, so it needs a clean git working tree first",
		Hint:    "commit or stash your changes (or commit the initial project if it isn't tracked yet), then re-run",
	}
}

// treeClean reports whether projectDir may be modified under the clean-tree
// rule: true outside a git repo, when git can't be consulted, or when the work
// tree has no pending changes. Only a repo that git can positively report as
// dirty is refused.
func treeClean(ctx context.Context, projectDir string) bool {
	root, ok := gitrepo.Root(ctx, projectDir)
	if !ok {
		return true // not a git repo — nothing to enforce
	}
	clean, err := gitrepo.IsClean(ctx, root)
	return err != nil || clean // can't tell, or already clean — proceed
}

// requireLogin fails fast with a friendly message when the user isn't logged in
// (a hard prerequisite for the graph calls init makes).
func requireLogin(ctx context.Context) error {
	if _, err := auth.Current(ctx); err != nil {
		return loginAwareErr(err)
	}
	return nil
}

// resolveOrg returns the active organization, prompting the org picker when none
// is selected (e.g. the user cancelled the switcher after login) — instead of
// bailing. The choice is persisted, like `driverforge org select`.
func resolveOrg(ctx context.Context, cl *driverforgeapi.Client) (orgctx.Org, error) {
	if org, err := orgctx.Current(); err == nil {
		return org, nil
	}

	orgs, err := cl.ListOrganizations(ctx)
	if err != nil {
		return orgctx.Org{}, loginAwareErr(err)
	}
	if len(orgs) == 0 {
		return orgctx.Org{}, &clierr.UserError{
			Title:   "No organizations",
			Message: "you don't belong to any organization yet",
			Hint:    urlLine("Create an organization", newOrgURL),
		}
	}
	if !ui.Interactive(flagNoTUI) {
		slugs := make([]string, len(orgs))
		for i, o := range orgs {
			slugs[i] = o.Slug
		}
		return orgctx.Org{}, newCmdErr(codeInputRequired,
			"No organization is active, and --org wasn't given.",
			"Pass one of the candidates to --org.").with("missing", []string{"--org"}).with("candidates", slugs).asUserError()
	}

	picked, err := pickOrg(orgs, "")
	if err != nil {
		return orgctx.Org{}, err
	}
	o := orgctx.Org{ID: picked.ID, Name: picked.Name, Slug: picked.Slug}
	return o, useOrg(o)
}

// useOrg makes o the active organization, like `driverforge org select`.
func useOrg(o orgctx.Org) error {
	if err := orgctx.SetCurrent(o); err != nil {
		return fmt.Errorf("saving organization: %w", err)
	}
	// The metrics client keyed the org at process start; re-key it so any
	// events emitted later in this run land on the org just chosen.
	metricsClient.SetOrg(o.ID)
	return nil
}

// gatherProject collects the project decision without mutating anything: pick an
// existing project to link, or gather the details for a new one (created later in
// the execute phase).
func gatherProject(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, org orgctx.Org) (projectPlan, error) {
	var projects []driverforgeapi.Project
	if err := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), "Loading your Driverforge projects…", func() (string, error) {
		ps, e := cl.ListProjects(ctx, org.ID)
		if e != nil {
			return "", loginAwareErr(e)
		}
		projects = ps
		return fmt.Sprintf("Found %d project%s in %s", len(ps), plural(len(ps)), org.Name), nil
	}); err != nil {
		return projectPlan{}, err
	}

	// With no projects yet, skip the existing/create question and go straight to
	// creation; otherwise ask first, then show the list only when "existing".
	create := len(projects) == 0
	if !create {
		mode, err := ui.PickOneDesc(
			"Which Driverforge project should this driver report to?",
			"Your driver's events, logs and errors are grouped under a project in Driverforge. Pick one you already have, or create a new one.",
			[]ui.Choice{
				{Label: "Use an existing project", Value: "existing"},
				{Label: "Create a new project", Value: "create"},
			}, "existing")
		if err != nil {
			return projectPlan{}, err
		}
		create = mode == "create"
	}

	if create {
		ci, err := gatherCreateIntent(ctx, out, cl, org)
		if err != nil {
			return projectPlan{}, err
		}
		fmt.Fprintln(out, ui.Success("Project: "+ci.name+" (new)"))
		return projectPlan{create: ci}, nil
	}

	chosen, err := pickExistingProject(projects)
	if err != nil {
		return projectPlan{}, err
	}
	fmt.Fprintln(out, ui.Success("Project: "+chosen.Name))
	return projectPlan{existing: &chosen}, nil
}

// linkProjectByRef resolves the --project ref to an org + project (skipping the
// pickers), persists the org as current, and resolves the project's API key
// (prompting if it has several). Echoes the captured org/project like the
// interactive path.
func linkProjectByRef(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, ref string) (orgctx.Org, projectPlan, error) {
	org, proj, err := resolveProjectRef(ctx, cl, ref)
	if err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	if err := useOrg(org); err != nil {
		return orgctx.Org{}, projectPlan{}, err
	}
	fmt.Fprintln(out, ui.Success("Organization: "+orgLabel(org.Name, org.Slug)))
	fmt.Fprintln(out, ui.Success("Project: "+proj.Name))
	return org, projectPlan{existing: &proj}, nil
}

// resolveProjectRef maps a --project value to its org + project. It accepts
// "<org-slug>/<project-slug>" (explicit) or a bare project slug (resolved via the
// user's projects when unambiguous).
func resolveProjectRef(ctx context.Context, cl *driverforgeapi.Client, ref string) (orgctx.Org, driverforgeapi.Project, error) {
	orgs, err := cl.ListOrganizations(ctx)
	if err != nil {
		return orgctx.Org{}, driverforgeapi.Project{}, loginAwareErr(err)
	}

	if orgSlug, projSlug, ok := strings.Cut(ref, "/"); ok {
		if orgSlug == "" || projSlug == "" {
			return orgctx.Org{}, driverforgeapi.Project{}, clierr.WithCode(clierr.UserT("Invalid --project",
				fmt.Sprintf("%q isn't a valid project reference", ref),
				"use <org-slug>/<project-slug>, or a project slug"), codeUsage)
		}
		o := findOrg(orgs, orgSlug)
		if o == nil {
			return orgctx.Org{}, driverforgeapi.Project{}, clierr.WithCode(clierr.UserT("Organization not found",
				fmt.Sprintf("no organization %q", orgSlug),
				"run `driverforge org list` to see your organizations"), codeOrgNotFound)
		}
		projects, err := cl.ListProjects(ctx, o.ID)
		if err != nil {
			return orgctx.Org{}, driverforgeapi.Project{}, loginAwareErr(err)
		}
		proj := findProjectBySlug(projects, projSlug)
		if proj == nil {
			return orgctx.Org{}, driverforgeapi.Project{}, clierr.WithCode(clierr.UserT("Project not found",
				fmt.Sprintf("no project %q in %s", projSlug, o.Slug),
				"check the slug, or create it with --create-project"), codeProjectNotFound)
		}
		return orgctx.Org{ID: o.ID, Name: o.Name, Slug: o.Slug}, *proj, nil
	}

	// Bare slug: resolve against all the user's projects; require it to be unique.
	all, err := cl.ListAllProjects(ctx)
	if err != nil {
		return orgctx.Org{}, driverforgeapi.Project{}, loginAwareErr(err)
	}
	var matches []driverforgeapi.Project
	for _, p := range all {
		if p.Slug == ref {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		return orgctx.Org{}, driverforgeapi.Project{}, clierr.WithCode(clierr.UserT("Project not found",
			fmt.Sprintf("no project with slug %q", ref),
			"check the slug, or use <org-slug>/<project-slug>"), codeProjectNotFound)
	case 1:
		proj := matches[0]
		o := findByID(orgs, proj.OrganizationID)
		if o == nil {
			return orgctx.Org{}, driverforgeapi.Project{}, &clierr.UserError{
				Title:   "Organization not found",
				Message: "couldn't resolve the project's organization",
				Hint:    "run `driverforge org list`",
			}
		}
		return orgctx.Org{ID: o.ID, Name: o.Name, Slug: o.Slug}, proj, nil
	default:
		return orgctx.Org{}, driverforgeapi.Project{}, clierr.WithCode(clierr.UserT("Ambiguous project",
			fmt.Sprintf("more than one project has the slug %q", ref),
			"qualify it: <org-slug>/<project-slug>, or add --org"), codeContextAmbiguous)
	}
}

// findProjectBySlug returns the project with the given slug, or nil.
func findProjectBySlug(projects []driverforgeapi.Project, slug string) *driverforgeapi.Project {
	for i := range projects {
		if projects[i].Slug == slug {
			return &projects[i]
		}
	}
	return nil
}

// pickExistingProject presents the org's projects and returns the chosen one.
func pickExistingProject(projects []driverforgeapi.Project) (driverforgeapi.Project, error) {
	choices := make([]ui.Choice, len(projects))
	for i, p := range projects {
		label := p.Name
		if p.Slug != "" {
			label += " (" + p.Slug + ")"
		}
		choices[i] = ui.Choice{Label: label, Value: p.ID}
	}
	picked, err := ui.PickOne("Which Driverforge project?", choices, projects[0].ID)
	if err != nil {
		return driverforgeapi.Project{}, err
	}
	for _, p := range projects {
		if p.ID == picked {
			return p, nil
		}
	}
	return driverforgeapi.Project{}, fmt.Errorf("selected project %q not found", picked)
}

// gatherCreateIntent collects platform + name + description for a new project,
// without creating it. A coming-soon platform routes to register-interest and
// loops back (or stops). Mirrors the web app's new-project drawer.
func gatherCreateIntent(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, org orgctx.Org) (*createIntent, error) {
	platforms, err := loadPlatforms(ctx, out, cl)
	if err != nil {
		return nil, err
	}

	for {
		platform, err := pickPlatform(platforms)
		if err != nil {
			return nil, err
		}

		// Coming-soon platforms can't host a project — offer the notify-me path,
		// then loop back to the picker or stop.
		if !platform.Supported {
			again, err := offerPlatformInterest(ctx, out, cl, org.ID, platform)
			if err != nil {
				return nil, err
			}
			if again {
				continue
			}
			return nil, projectCreationAbandoned(out)
		}
		fmt.Fprintln(out, ui.Success("Platform: "+platform.Name))

		name, err := ui.PromptText("Project name", "")
		if err != nil {
			return nil, err
		}
		if name == "" {
			return nil, &clierr.UserError{Title: "Project name required", Message: "a new project needs a name"}
		}
		description, err := ui.PromptText("Description (optional)", "")
		if err != nil {
			return nil, err
		}
		return &createIntent{name: name, description: description, platform: platform}, nil
	}
}

// loadPlatforms fetches the available platforms behind a spinner.
func loadPlatforms(ctx context.Context, out io.Writer, cl *driverforgeapi.Client) ([]driverforgeapi.Platform, error) {
	var platforms []driverforgeapi.Platform
	err := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), "Loading platforms…", func() (string, error) {
		ps, e := cl.ListPlatforms(ctx)
		if e != nil {
			return "", loginAwareErr(e)
		}
		platforms = ps
		return "Platforms loaded", nil
	})
	return platforms, err
}

// pickPlatform presents the selectable (non-hidden) platforms, marking
// coming-soon ones, and defaults to the first supported platform.
func pickPlatform(platforms []driverforgeapi.Platform) (driverforgeapi.Platform, error) {
	byID := make(map[string]driverforgeapi.Platform, len(platforms))
	var choices []ui.Choice
	def := ""
	for _, p := range platforms {
		if p.Hidden {
			continue
		}
		byID[p.ID] = p
		label := p.Name
		if !p.Supported && p.ComingSoon {
			label += " (coming soon)"
		}
		choices = append(choices, ui.Choice{Label: label, Value: p.ID})
		if def == "" && p.Supported {
			def = p.ID
		}
	}
	if len(choices) == 0 {
		return driverforgeapi.Platform{}, &clierr.UserError{Title: "No platforms available", Message: "no automation platforms are available to target"}
	}
	if def == "" {
		def = choices[0].Value
	}
	picked, err := ui.PickOneDesc("Which platform is this driver for?", "Driverforge targets these automation platforms.", choices, def)
	if err != nil {
		return driverforgeapi.Platform{}, err
	}
	return byID[picked], nil
}

// offerPlatformInterest registers interest in a coming-soon platform (if the user
// wants) and asks whether to pick a different one. again=true loops the caller
// back to platform selection.
func offerPlatformInterest(ctx context.Context, out io.Writer, cl *driverforgeapi.Client, orgID string, p driverforgeapi.Platform) (again bool, err error) {
	notify, err := ui.Confirm(
		fmt.Sprintf("%s isn't available yet. Get notified when it's ready?", p.Name),
		"We'll let you know when Driverforge supports it.", true)
	if err != nil {
		return false, err
	}
	if notify {
		if err := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI),
			fmt.Sprintf("Registering interest in %s…", p.Name),
			func() (string, error) {
				if e := cl.CreatePlatformInterest(ctx, orgID, p.ID); e != nil {
					return "", loginAwareErr(e)
				}
				return fmt.Sprintf("We'll email you when %s support lands", p.Name), nil
			}); err != nil {
			return false, err
		}
	}
	return ui.Confirm("Choose a different platform?", "", true)
}

// projectCreationAbandoned reports that no project was created and stops init
// cleanly (no error card).
func projectCreationAbandoned(out io.Writer) error {
	fmt.Fprintln(out, "\nNo project created — re-run driverforge init when you're ready.")
	return clierr.Silent()
}

// loggerGlobalRe matches a logger used as `<name>:` or `<name>.` for the common
// logger global names, capturing which one so the prompt can name it.
var loggerGlobalRe = regexp.MustCompile(`(?m)\b(Log|Logger|logger|_logger)\s*[:.]`)

// resolveLoggerGlobal works out which logger global (if any) Driverforge should forward
// log output from. Driverforge never creates a logger — it only forwards output from
// one the driver already has — so the prompts are framed around an *existing*
// logger, default to "none" when nothing is detected, and link the interface docs
// so the user can check their logger is compatible.
func resolveLoggerGlobal(srcDir, entryRel string) (string, error) {
	detected := detectLogger(srcDir, entryRel)

	// Found a likely logger — confirm forwarding it.
	if detected != "" {
		on, err := ui.Confirm(
			"Forward your driver's logs to Driverforge?",
			fmt.Sprintf("It looks like your driver already has a logger (%s). Do you want to forward logs from it to Driverforge automatically, alongside events and errors? Your logger needs to implement the expected interface.\n\n%s", detected, urlLine("Documentation", loggerDocsURL)),
			true)
		if err != nil {
			return "", err
		}
		if on {
			return detected, nil
		}
		return "", nil
	}

	// Nothing detected — ask whether they have a logger at all before naming it.
	has, err := ui.Confirm(
		"Does your driver have a logger Driverforge should forward?",
		"Driverforge can forward your driver's own log output to the dashboard, alongside events and errors. It won't create a logger for you — skip this if your driver doesn't log through a global. The logger must implement the expected interface.\n\n"+urlLine("Documentation", loggerDocsURL),
		false)
	if err != nil {
		return "", err
	}
	if !has {
		return "", nil
	}

	name, err := ui.PromptOptional(
		"What's your logger's global name?",
		`The variable your driver logs through — e.g. Log, as in Log:Info("…").`,
		"")
	if err != nil {
		return "", err
	}
	if name != "" && !luaGlobalRe.MatchString(name) {
		return "", &clierr.UserError{
			Title:   "Invalid logger name",
			Message: fmt.Sprintf("%q isn't a valid Lua global", name),
			Hint:    "use a name like Log or my.logger — letters, digits, _ and . only",
		}
	}
	return name, nil
}

// luaGlobalRe matches a valid Lua global/field-access name. It guards the
// user-entered logger name, which is spliced verbatim into generated Lua
// (Driverforge:Init(…, { logger = <name> })) — an unvalidated value could inject code.
var luaGlobalRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)*$`)

// detectLogger returns the conventional logger global used in the entry file
// (Log / Logger / logger / _logger, called as name: or name.), or "" if none is
// found — a differently-named logger isn't auto-detected and is named by the user.
func detectLogger(srcDir, entryRel string) string {
	src, err := os.ReadFile(filepath.Join(srcDir, entryRel))
	if err != nil {
		return ""
	}
	// Match against code only: a logger name that survives just in a comment or a
	// string (`-- Log:Info was removed`) must not trigger the "forward logs from
	// (Log)?" prompt — a real logger is always *called* in code.
	if m := loggerGlobalRe.FindSubmatch(initdriver.CodeOnly(src)); m != nil {
		return string(m[1])
	}
	return ""
}

// loginAwareErr turns a not-logged-in failure into a friendly re-login prompt,
// leaving other errors untouched.
func loginAwareErr(err error) error {
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return clierr.WithCode(clierr.WrapT(err, "Not logged in",
			"driverforge init needs you to be logged in",
			"run `driverforge login`"), codeAuthRequired)
	}
	return err
}

// freeBranchName returns base, or base-2/base-3/… when earlier names are taken.
func freeBranchName(ctx context.Context, root, base string) string {
	if !gitrepo.BranchExists(ctx, root, base) {
		return base
	}
	for i := 2; i < 100; i++ {
		n := fmt.Sprintf("%s-%d", base, i)
		if !gitrepo.BranchExists(ctx, root, n) {
			return n
		}
	}
	return base
}
