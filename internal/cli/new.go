package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/scaffold"
	"github.com/driverforge/cli/internal/telemetry"
	"github.com/driverforge/cli/internal/ui"
)

// newOpts are `new`'s flags: where the template comes from, its variables, and
// init's answers, which `new` passes straight to the init it runs.
type newOpts struct {
	init     initOpts
	repo     string
	template string
	set      map[string]string
	noInit   bool
}

type newOut struct {
	Name     string   `json:"name"`
	Dir      string   `json:"dir"`
	Repo     string   `json:"repo"`
	Template string   `json:"template"`
	Files    []string `json:"files"`
	Init     *initOut `json:"init,omitempty"`
	SetupURL string   `json:"setup_url,omitempty"`
}

// newFrom tells the dev tools the project was just made from a starter
// template, so its setup page can skip what init already did.
const newFrom = "cli-starter-kit"

func newNewCmd() *cobra.Command {
	var o newOpts
	cmd := &cobra.Command{
		Use:   "new <name>",
		Short: "Create a driver project from a template",
		Long: "Create a driver project from a template, then set it up for Driverforge.\n\n" +
			"The template is fetched from a public GitHub repository — by default the\n" +
			"official " + scaffold.DefaultRepo + " — into a new directory called <name>, with the\n" +
			"project's name filled in. Then `driverforge init` runs in it, asking the same\n" +
			"questions (or taking the same flags) to embed the latest SDK and link or\n" +
			"create the Driverforge project.\n\n" +
			"Use --repo to start from your own templates: a GitHub owner/repo (public),\n" +
			"or any git URL — GitLab, Bitbucket, ssh, a private repository — cloned with\n" +
			"your own git credentials. Add @ref (GitHub) or #ref (URL) for a branch or\n" +
			"tag. The repository holds one directory per template, or a single template\n" +
			"at its root. A template is a\n" +
			"driver project whose files may contain ${name}, ${slug}, ${id}, ${title} and\n" +
			"${year} (the name as typed, my-driver, my_driver, My Driver and the current\n" +
			"year), plus any variables its template.json declares, set with --set. Write\n" +
			"$${ for a literal ${.",
		Example: "  driverforge new my-new-driver\n" +
			"  driverforge new my-new-driver --create-project \"My New Driver\" --versioning date \\\n" +
			"      --no-build-configs --no-logger\n" +
			"  driverforge new my-driver --repo https://gitlab.com/acme/templates.git#v2\n" +
			"  driverforge new acme-thermostat --repo acme/driver-templates@v2 --template thermostat --set vendor=Acme\n" +
			"  driverforge new scratch --no-init",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.init.sdkDirSet = cmd.Flags().Changed("sdk-dir")
			doc, err := runNew(cmd.Context(), textOut(cmd), args[0], o)
			if err != nil {
				return err
			}
			return emit(cmd, doc, func(w io.Writer) error {
				fmt.Fprintf(w, "\nNext: cd %s\n", shellArg(args[0]))
				return nil
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&o.repo, "repo", scaffold.DefaultRepo, "repository holding the templates: owner/repo[@ref] on GitHub, or any git URL[#ref] (GitLab, Bitbucket, ssh, private…)")
	f.StringVar(&o.template, "template", "", "template directory within the repository (default \""+scaffold.DefaultTemplate+"\", or the repository root if it is a single template)")
	f.StringToStringVar(&o.set, "set", nil, "set a variable the template declares, e.g. --set vendor=Acme (repeatable)")
	f.BoolVar(&o.noInit, "no-init", false, "create the project without setting it up for Driverforge")
	addInitAnswerFlags(f, &o.init)
	return cmd
}

// newPlan is everything `new` settles before it touches the network or the
// disk: where the project goes, what it is called, where the template comes
// from, and the answers init will be given.
type newPlan struct {
	dest     string
	name     string
	src      scaffold.Source
	builtins map[string]string
	init     initOpts
}

// planNew validates the request: the name, the repository, and — unless
// --no-init — everything init will need, so a missing flag or a missing login
// fails before anything is created.
func planNew(ctx context.Context, arg string, o newOpts) (newPlan, error) {
	usage := func(msg, hint string) error { return clierr.WithCode(clierr.User(msg, hint), codeUsage) }
	dest, err := filepath.Abs(arg)
	if err != nil {
		return newPlan{}, err
	}
	p := newPlan{dest: dest, name: filepath.Base(dest)}
	if p.builtins, err = scaffold.Names(p.name, time.Now()); err != nil {
		return newPlan{}, usage(fmt.Sprintf("%q isn't a usable project name", p.name), "use letters and digits, such as my-new-driver")
	}
	if p.src, err = scaffold.ParseSource(o.repo); err != nil {
		return newPlan{}, clierr.WithCode(err, codeUsage)
	}
	if o.noInit {
		if o.init != (initOpts{sdkDir: o.init.sdkDir, sdkDirSet: o.init.sdkDirSet}) {
			return newPlan{}, usage("--no-init can't be combined with init's flags", "drop --no-init, or the flags that answer init's questions")
		}
		return p, nil
	}
	// A new project is reviewed by being the template, so init applies without
	// a diff, and there's no branch to cut: the directory is new.
	p.init = o.init
	p.init.yes, p.init.noBranch, p.init.fresh = true, true, true
	return p, checkInitAnswers(ctx, p.init)
}

// checkInitAnswers fails, before anything is created, on whatever would stop
// init: contradictory flags, no login, or questions it can't ask without a
// terminal.
func checkInitAnswers(ctx context.Context, ia initOpts) error {
	if err := ia.validate(); err != nil {
		return err
	}
	if err := requireLogin(ctx); err != nil {
		return err
	}
	if ui.Interactive(flagNoTUI) {
		return nil
	}
	if missing := ia.missingFlags(false); len(missing) > 0 {
		return newCmdErr(codeInputRequired,
			"driverforge new can't ask its questions without a terminal, and these weren't answered: "+strings.Join(missing, ", ")+".",
			"Pass them as flags (see `driverforge new --help`).").with("missing", missing).asUserError()
	}
	return nil
}

// fetchTemplate downloads the repository and picks the template out of it,
// under a progress frame when there is somewhere to draw one.
func fetchTemplate(ctx context.Context, out io.Writer, src scaffold.Source, name string) (scaffold.Template, error) {
	var tmpl scaffold.Template
	fetch := func(ctx context.Context) error {
		repo, err := scaffold.Fetch(ctx, src)
		if err != nil {
			return err
		}
		tmpl, err = repo.Select(name)
		return err
	}
	var err error
	if ui.Interactive(flagNoTUI) || structured() {
		_, err = runFrame(ctx, out, ui.FrameOptions{Step: "Fetching template…"}, func(ctx context.Context, _ ui.Progress) error { return fetch(ctx) })
	} else {
		err = fetch(ctx)
	}
	return tmpl, err
}

// templateVars resolves the template's variables: the built-ins, its defaults
// and --set, then asks (in a terminal) for any required one still missing.
// Without a terminal those fail together as input_required.
func templateVars(tmpl scaffold.Template, builtins, set map[string]string) (map[string]string, error) {
	vars, missing, err := scaffold.Resolve(tmpl.Manifest, builtins, set)
	if err != nil {
		return nil, clierr.WithCode(clierr.Wrap(err, "unknown template variable", "see the template's template.json for the variables it accepts"), codeUsage)
	}
	if ui.Interactive(flagNoTUI) {
		for _, k := range missing {
			title := k
			if d := tmpl.Manifest.Variables[k].Description; d != "" {
				title = d
			}
			if vars[k], err = ui.PromptText(title, ""); err != nil {
				return nil, err
			}
		}
	}
	if left := missingVars(vars, tmpl.Manifest); len(left) > 0 {
		return nil, newCmdErr(codeInputRequired,
			"this template needs values for: "+strings.Join(left, ", ")+".",
			"Pass each as --set name=value.").with("missing", left).asUserError()
	}
	return vars, nil
}

// runNew creates the project directory from the template and, unless --no-init,
// runs init in it. Nothing is created until the template has downloaded and
// rendered cleanly. If init then fails the project stays: it is a valid driver,
// and `driverforge init` finishes the job from inside it.
func runNew(ctx context.Context, out io.Writer, arg string, o newOpts) (newOut, error) {
	p, err := planNew(ctx, arg, o)
	if err != nil {
		return newOut{}, err
	}
	tmpl, err := fetchTemplate(ctx, out, p.src, o.template)
	if err != nil {
		return newOut{}, err
	}
	desc := tmpl.Manifest.Description
	if desc == "" {
		desc = "template " + templateLabel(tmpl)
	}
	fmt.Fprintln(out, ui.Success("Template: "+desc+" ("+p.src.String()+")"))

	vars, err := templateVars(tmpl, p.builtins, o.set)
	if err != nil {
		return newOut{}, err
	}
	files, err := scaffold.Render(tmpl.Files, vars)
	if err != nil {
		return newOut{}, clierr.WithCode(clierr.Wrap(err, "the template couldn't be rendered", "this is a problem in the template — report it to its author"), codeFailed)
	}
	if err := scaffold.Write(p.dest, files); err != nil {
		return newOut{}, err
	}
	doc := newOut{Name: p.name, Dir: p.dest, Repo: p.src.String(), Template: templateLabel(tmpl), Files: make([]string, 0, len(files))}
	for _, f := range files {
		doc.Files = append(doc.Files, f.Path)
	}
	fmt.Fprintln(out, ui.Success(fmt.Sprintf("Created %s (%d file%s)", arg, len(files), plural(len(files)))))
	if o.noInit {
		return doc, nil
	}
	return finishNew(ctx, out, arg, p, doc)
}

// finishNew runs init in the new project and, once it has applied, shows the
// link to the project's dev tools.
func finishNew(ctx context.Context, out io.Writer, arg string, p newPlan, doc newOut) (newOut, error) {
	initDoc, err := runInit(ctx, out, p.dest, p.init)
	doc.Init = &initDoc
	if err != nil {
		fmt.Fprintf(out, "\n%s was created, but setting it up for Driverforge didn't finish. Run `driverforge init` inside it to retry.\n", arg)
		return doc, err
	}
	if initDoc.Applied && initDoc.Org.Slug != "" && initDoc.Project.Slug != "" {
		doc.SetupURL = telemetry.SetupURL(initDoc.Org.Slug, initDoc.Project.Slug, newFrom, arg)
		offerDevTools(out, doc.SetupURL)
	}
	return doc, nil
}

// offerDevTools prints the dev-tools link and, in a terminal, offers to open it.
func offerDevTools(out io.Writer, link string) {
	fmt.Fprintf(out, "\n%s\n", urlLine("Dev tools", link))
	if !ui.Interactive(flagNoTUI) {
		return
	}
	open, err := ui.Confirm("Open the dev tools in your browser?", "It shows what to do next for this project.", true)
	if err != nil || !open {
		return
	}
	if err := auth.OpenBrowser(link); err != nil {
		fmt.Fprintln(out, ui.Skipped("Couldn't open a browser — use the link above"))
	}
}

// missingVars lists required variables still without a value.
func missingVars(vars map[string]string, m scaffold.Manifest) []string {
	var left []string
	for _, k := range m.Declared() {
		if _, ok := vars[k]; !ok {
			left = append(left, k)
		}
	}
	return left
}

func templateLabel(t scaffold.Template) string {
	if t.Path == "" {
		return "(repository root)"
	}
	return t.Path
}

// shellArg quotes a path for display in a `cd` hint when it needs it.
func shellArg(p string) string {
	if strings.ContainsAny(p, " \t'\"$`\\") {
		return "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return p
}
