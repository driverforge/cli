package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/gitrepo"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/versioning"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

// initOpts are init's answers given as flags. Each one stands in for a
// question the wizard would ask; a question without its flag is asked in a
// terminal and, without one, reported as missing before anything happens.
type initOpts struct {
	project       string // link this existing project: <org-slug>/<project-slug> or a slug
	createProject string // create a project with this name
	org           string // the organization to create in, or to qualify a bare --project slug
	platform      string
	description   string

	versioning     string
	buildConfigs   bool
	noBuildConfigs bool
	logger         string
	noLogger       bool

	branch       string
	noBranch     bool
	commitMsg    string
	commitMsgSet bool // --commit-message was given; empty means don't commit

	yes    bool
	dryRun bool
}

// validate rejects flags that contradict each other or answer a question
// that isn't being asked.
func (o initOpts) validate() error {
	usage := func(msg, hint string) error {
		return clierr.WithCode(clierr.User(msg, hint), codeUsage)
	}
	switch {
	case o.project != "" && o.createProject != "":
		return usage("--project and --create-project can't be used together", "link an existing project with --project, or create one with --create-project")
	case o.createProject == "" && (o.platform != "" || o.description != ""):
		return usage("--platform and --description only apply with --create-project", "add --create-project <name>, or drop them")
	case o.org != "" && strings.Contains(o.project, "/"):
		return usage("--project already names its organization", "pass --project <org-slug>/<project-slug>, or --org with a bare project slug")
	case o.org != "" && o.project == "" && o.createProject == "":
		return usage("--org needs --project or --create-project", "name the project to link or create")
	case o.versioning != "" && !versioning.Valid(o.versioning):
		return usage(fmt.Sprintf("--versioning must be %s or %s", versioning.Date, versioning.Integer), "date bumps to YYYYMMDDNN; integer adds one")
	case o.buildConfigs && o.noBuildConfigs:
		return usage("--build-configs and --no-build-configs can't be used together", "pass one of them")
	case o.logger != "" && o.noLogger:
		return usage("--logger and --no-logger can't be used together", "pass one of them")
	case o.logger != "" && !luaGlobalRe.MatchString(o.logger):
		return usage(fmt.Sprintf("--logger %q isn't a valid Lua global", o.logger), "use a name like Log or my.logger — letters, digits, _ and . only")
	case o.branch != "" && o.noBranch:
		return usage("--branch and --no-branch can't be used together", "pass one of them")
	case o.commitMsgSet && o.branch == "":
		return usage("--commit-message needs --branch", "init commits only onto the branch it creates")
	case o.yes && o.dryRun:
		return usage("--yes and --dry-run can't be used together", "preview with --dry-run, then apply with --yes")
	}
	return nil
}

// missingAnswers lists the flags a run without a terminal still needs, so one
// failure names them all instead of one per attempt. detected carries what
// init found in the driver, for choosing the answers.
func (o initOpts) missingAnswers(ctx context.Context, proj project.Project, entryRel string) (missing []string, detected map[string]string) {
	if o.project == "" && o.createProject == "" {
		missing = append(missing, "--project or --create-project")
	}
	if o.project == "" && o.org == "" {
		if _, err := orgctx.Current(); err != nil {
			missing = append(missing, "--org")
		}
	}
	if o.versioning == "" {
		missing = append(missing, "--versioning")
	}
	if !o.buildConfigs && !o.noBuildConfigs {
		missing = append(missing, "--build-configs or --no-build-configs")
	}
	if o.logger == "" && !o.noLogger {
		missing = append(missing, "--logger or --no-logger")
	}
	if !o.noBranch && o.branch == "" && branchable(ctx, proj.Dir) {
		missing = append(missing, "--branch or --no-branch")
	}
	if !o.yes && !o.dryRun {
		missing = append(missing, "--dry-run or --yes")
	}
	_, scheme := detectScheme(proj)
	detected = map[string]string{"versioning": scheme}
	if l := detectLogger(proj.SrcDir, entryRel); l != "" {
		detected["logger"] = l
	}
	return missing, detected
}

// inputRequired reports the missing answers as one failure.
func inputRequired(missing []string, detected map[string]string) error {
	return newCmdErr(codeInputRequired,
		"driverforge init can't ask its questions without a terminal, and these weren't answered: "+strings.Join(missing, ", ")+".",
		"Pass them as flags (see `driverforge init --help`); preview with --dry-run before applying with --yes.").
		with("missing", missing).with("detected", detected).asUserError()
}

// branchable reports whether init could put its changes on a new branch: the
// project is in a git repository with at least one commit.
func branchable(ctx context.Context, dir string) bool {
	root, ok := gitrepo.Root(ctx, dir)
	return ok && gitrepo.HasCommits(ctx, root)
}

// detectScheme reads the authored <version> and the versioning scheme it
// looks like. A driver with no version yet defaults to date-based.
func detectScheme(proj project.Project) (current, scheme string) {
	if data, err := os.ReadFile(filepath.Join(proj.SrcDir, "driver.xml")); err == nil {
		v, _ := driverxml.ReadVersion(data)
		current = strings.TrimSpace(v)
	}
	if current == "" {
		return "", versioning.Date
	}
	return current, versioning.Detect(current)
}

// initBranchIntent is gatherBranchIntent answered by --branch or --no-branch
// when given.
func initBranchIntent(ctx context.Context, out io.Writer, dir string, o initOpts) (branch, commitMsg string, err error) {
	switch {
	case o.noBranch:
		fmt.Fprintln(out, ui.Skipped("Git branch: none"))
		return "", "", nil
	case o.branch == "":
		return gatherBranchIntent(ctx, out, dir, "driverforge-init", defaultInitCommitMessage)
	}
	root, ok := gitrepo.Root(ctx, dir)
	if !ok || !gitrepo.HasCommits(ctx, root) {
		return "", "", clierr.WithCode(clierr.User("--branch needs a git repository with at least one commit",
			"commit the project first, or pass --no-branch"), codeUsage)
	}
	if err := checkBranchName(ctx, root, o.branch); err != nil {
		return "", "", err
	}
	fmt.Fprintln(out, ui.Success("Git branch: "+o.branch))
	commitMsg = defaultInitCommitMessage
	if o.commitMsgSet {
		commitMsg = o.commitMsg
	}
	if commitMsg == "" {
		fmt.Fprintln(out, ui.Skipped("Commit: none (changes left uncommitted)"))
	} else {
		fmt.Fprintln(out, ui.Success("Commit: "+commitMsg))
	}
	return o.branch, commitMsg, nil
}

// choosePlatform resolves --platform (an id or name), or the only supported
// platform when there's one, or asks.
func choosePlatform(platforms []driverforgeapi.Platform, flag string) (driverforgeapi.Platform, error) {
	var supported []driverforgeapi.Platform
	for _, p := range platforms {
		if !p.Hidden && p.Supported {
			supported = append(supported, p)
		}
	}
	names := make([]string, len(supported))
	for i, p := range supported {
		names[i] = p.ID
	}
	if flag != "" {
		for _, p := range platforms {
			if p.Hidden || (!strings.EqualFold(p.ID, flag) && !strings.EqualFold(p.Name, flag)) {
				continue
			}
			if !p.Supported {
				return driverforgeapi.Platform{}, newCmdErr(codeUsage,
					fmt.Sprintf("%s isn't available yet.", p.Name),
					"Choose a supported platform.").with("candidates", names).asUserError()
			}
			return p, nil
		}
		return driverforgeapi.Platform{}, newCmdErr(codeUsage,
			fmt.Sprintf("There's no platform %q.", flag),
			"Pass one of the candidates to --platform.").with("candidates", names).asUserError()
	}
	if len(supported) == 1 {
		return supported[0], nil
	}
	if !ui.Interactive(flagNoTUI) {
		return driverforgeapi.Platform{}, newCmdErr(codeInputRequired,
			"More than one platform is available, and --platform wasn't given.",
			"Pass one of the candidates to --platform.").with("missing", []string{"--platform"}).with("candidates", names).asUserError()
	}
	return pickPlatform(platforms)
}

// orgBySlug resolves --org among the user's organizations.
func orgBySlug(ctx context.Context, cl *driverforgeapi.Client, slug string) (orgctx.Org, error) {
	orgs, err := cl.ListOrganizations(ctx)
	if err != nil {
		return orgctx.Org{}, loginAwareErr(err)
	}
	o := findOrg(orgs, slug)
	if o == nil {
		return orgctx.Org{}, clierr.WithCode(clierr.UserT("Organization not found",
			fmt.Sprintf("no organization %q", slug),
			"run `driverforge org list` to see your organizations"), codeOrgNotFound)
	}
	return orgctx.Org{ID: o.ID, Name: o.Name, Slug: o.Slug}, nil
}

// initOut is what init reports in structured output. Applied is false for a
// dry run, and for a project that was already set up.
type initOut struct {
	Applied       bool            `json:"applied"`
	DryRun        bool            `json:"dry_run"`
	AlreadySetUp  bool            `json:"already_set_up"`
	Org           initOrgOut      `json:"org"`
	Project       initProjectOut  `json:"project"`
	SDKVersion    string          `json:"sdk_version"`
	Versioning    string          `json:"versioning"`
	BuildConfigs  bool            `json:"build_configs"`
	Logger        string          `json:"logger"`
	Branch        string          `json:"branch"`
	CommitMessage string          `json:"commit_message"`
	Changes       []initChangeOut `json:"changes"`
	Warnings      []warning       `json:"warnings"`
}

type initOrgOut struct {
	Slug string `json:"slug"`
	Name string `json:"name,omitempty"`
}

// initProjectOut is the project the driver reports to. Action is "create" or
// "link"; a dry run that creates has no id or slug yet.
type initProjectOut struct {
	Action   string `json:"action"`
	ID       string `json:"id,omitempty"`
	Slug     string `json:"slug,omitempty"`
	Name     string `json:"name"`
	Platform string `json:"platform,omitempty"`
}

// initChangeOut is one file init writes. Diff is omitted for the vendored SDK
// bundle.
type initChangeOut struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // create or modify
	Diff string `json:"diff,omitempty"`
}

func newInitOut() initOut {
	return initOut{Changes: []initChangeOut{}, Warnings: []warning{}}
}

// setProject records the project plan, or the project once it exists.
func (d *initOut) setProject(pp projectPlan, linked *driverforgeapi.Project) {
	switch {
	case linked != nil:
		d.Project.ID, d.Project.Slug, d.Project.Name = linked.ID, linked.Slug, linked.Name
	case pp.existing != nil:
		d.Project.ID, d.Project.Slug, d.Project.Name = pp.existing.ID, pp.existing.Slug, pp.existing.Name
	default:
		d.Project.Name = pp.create.name
	}
	d.Project.Action = "link"
	if pp.create != nil {
		d.Project.Action = "create"
		d.Project.Platform = pp.create.platform.ID
	}
}

// setChanges records the files init writes, with a plain diff of each.
func (d *initOut) setChanges(changes []initdriver.Change) {
	d.Changes = d.Changes[:0]
	for _, c := range changes {
		ch := initChangeOut{Path: filepath.ToSlash(c.RelPath), Kind: "modify"}
		if c.Kind == initdriver.Create {
			ch.Kind = "create"
		}
		if !isSDKBundle(c.RelPath) {
			ch.Diff = ansi.Strip(ui.RenderFileDiffBody(c.Old, c.New))
		}
		d.Changes = append(d.Changes, ch)
	}
}
