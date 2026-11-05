package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"text/tabwriter"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/agentrelease"
	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/initdriver"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/internal/selfupdate"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/updatecheck"
)

// newUpgradeCmd is the single upgrade surface: a status view of every component
// — the CLI, the selected controller's agent, and this driver's embedded SDK —
// showing current/latest and whether an update is available. Interactive: a
// picker to select and apply the actionable ones (the only upgrade path for
// the agent and the SDK). Non-interactive: it reports only — upgrades act on
// a connected controller or the working tree, never from CI.
func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Show and apply available upgrades (CLI, agent, SDK)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if structured() {
				return emit(cmd, upgradeReport(probeComponents(cmd.Context(), textOut(cmd))), noText)
			}
			return runUpgrade(cmd.Context(), cmd.OutOrStdout())
		},
	}
}

// compState classifies a component's update status for display.
type compState int

const (
	stateUpToDate compState = iota
	stateUpdate             // a newer version is available
	stateUnknown            // couldn't determine (unreachable, sign-in issue, …)
	stateNA                 // not applicable in this context (no controller, not a driver)
	stateBlocked            // a newer version is available but can't be applied right now
)

// sdkDirtyTreeNote is what the SDK row says when an update exists but the
// project's git working tree is dirty. The upgrade rewrites tracked files, so
// it needs a clean tree the same way init does; rather than let the row be
// selected and then refuse mid-run (after the CLI and agent upgrades have
// already happened), the picker withholds it and says what would unblock it.
const sdkDirtyTreeNote = "Commit or stash your changes to upgrade the SDK."

// sdkReleaseNotesURL is the GitHub release page for an SDK version (tags are
// v-prefixed, e.g. v0.5.0) — the picker's release-notes link.
const sdkReleaseNotesURL = "https://github.com/driverforge/control4-sdk/releases/tag/v%s"

// compRow is one row of the upgrade status table. run is set only for components
// driverforge can actually upgrade (agent, SDK) — the CLI is advisory (a package manager
// owns the binary), so it carries a cmd hint but no run.
type compRow struct {
	name     string
	detail   string // parenthetical context, e.g. the agent's controller label
	current  string
	latest   string
	note     string // status text, e.g. "up to date" / "no controller selected"
	cmd      string // command hint shown alongside an available update
	notesURL string // release-notes link for the picker (SDK only today)
	state    compState
	run      func(ctx context.Context) error
}

// probeComponents checks the CLI, the selected controller's agent and this
// driver's SDK. out receives only what an upgrade a row runs prints.
func probeComponents(ctx context.Context, out io.Writer) []compRow {
	// The three probes are independent network round-trips (CLI release store,
	// controller agent, SDK release store); run them concurrently so the table
	// renders in the slowest single probe's time, not their sum. Each writes
	// only its own row (out is used only inside the deferred run funcs), so no
	// synchronisation beyond the slice is needed.
	rows := make([]compRow, 3)
	builders := [](func() compRow){
		func() compRow { return cliRow(ctx, out) },
		func() compRow { return agentRow(ctx, out) },
		func() compRow { return sdkRow(ctx, out) },
	}
	var wg sync.WaitGroup
	for i := range builders {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rows[i] = builders[i]()
		}(i)
	}
	wg.Wait()
	return rows
}

func runUpgrade(ctx context.Context, out io.Writer) error {
	rows := probeComponents(ctx, out)

	hasUpdate := false
	runnable := false
	for _, r := range rows {
		if r.state == stateUpdate {
			hasUpdate = true
			if r.run != nil {
				runnable = true
			}
		}
	}

	// Non-interactive: report only — never mutate from a pipe/CI.
	if !ui.Interactive(flagNoTUI) {
		renderUpgradeTable(out, rows)
		if runnable {
			fmt.Fprintln(out, "\n(run a listed command to upgrade)")
		}
		return nil
	}
	// Nothing to do: the table is the status report.
	if !hasUpdate {
		renderUpgradeTable(out, rows)
		return nil
	}

	// Updates available: the interactive picker replaces the table — the same
	// rows, with the updatable ones (including the CLI) selectable.
	chosen, err := pickUpgrades(ctx, rows)
	if err != nil {
		return err
	}
	cliChosen := false
	for _, i := range chosen {
		r := rows[i]
		if r.run == nil {
			// A row that can't act on itself — today only the CLI, when its
			// binary isn't writable. Explain how after the real upgrades run.
			cliChosen = true
			continue
		}
		if err := r.run(ctx); err != nil {
			return err
		}
	}
	if cliChosen {
		cli := rows[0] // cliRow is always the first probe
		ui.Fprintln(out, "\n"+ui.InfoBlock(
			fmt.Sprintf("driverforge CLI %s is available", cli.latest),
			"The CLI can't upgrade itself — run:",
			"  "+cli.cmd))
	}
	return nil
}

// pickUpgrades runs the interactive component picker and returns the indices
// of the rows the user chose to upgrade (empty on decline/quit).
func pickUpgrades(ctx context.Context, rows []compRow) ([]int, error) {
	m := ui.NewUpgradePicker(buildUpgradeRows(rows), auth.OpenBrowser)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return nil, ui.ExpectedIfAborted(err)
	}
	if m.Err() != nil {
		return nil, ui.ExpectedIfAborted(m.Err())
	}
	if m.Aborted() {
		return nil, nil
	}
	return m.Chosen(), nil
}

// buildUpgradeRows maps the probed component rows onto the picker's display
// rows: the parenthetical detail (a controller label) folds into the note, and
// the state collapses to the picker's display classification.
func buildUpgradeRows(rows []compRow) []ui.UpgradeRow {
	out := make([]ui.UpgradeRow, len(rows))
	for i, r := range rows {
		note := r.note
		if r.detail != "" {
			note += " (" + r.detail + ")"
		}
		state := ui.UpgradeNA
		switch r.state {
		case stateUpdate:
			state = ui.UpgradeAvailable
		case stateBlocked:
			state = ui.UpgradeBlocked
		case stateUpToDate:
			state = ui.UpgradeCurrent
		case stateUnknown:
			state = ui.UpgradeUnknown
		}
		out[i] = ui.UpgradeRow{
			Name:     r.name,
			Current:  r.current,
			Latest:   r.latest,
			Note:     note,
			State:    state,
			NotesURL: r.notesURL,
		}
	}
	return out
}

// renderUpgradeTable prints the rows as an aligned table. Only the trailing status
// column is colored, so the ANSI codes never throw off tabwriter's alignment.
func renderUpgradeTable(out io.Writer, rows []compRow) {
	fmt.Fprintln(out)
	tw := tabwriter.NewWriter(out, 0, 0, 3, ' ', 0)
	fmt.Fprintln(tw, "COMPONENT\tCURRENT\tLATEST\tSTATUS")
	for _, r := range rows {
		name := r.name
		if r.detail != "" {
			name += " (" + r.detail + ")"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, r.current, r.latest, statusCell(r))
	}
	_ = tw.Flush()
}

// statusCell renders the colored icon + note (+ command hint) for a row. It is the
// last column, so its ANSI styling doesn't affect column alignment.
func statusCell(r compRow) string {
	icon, paint := "–", ui.StatusMuted // stateNA
	switch r.state {
	case stateUpToDate:
		icon, paint = "✓", ui.StatusOK
	case stateUpdate, stateBlocked:
		icon, paint = "⬆", ui.StatusWarn
	case stateUnknown:
		icon, paint = "⚠", ui.StatusWarn
	}
	text := r.note
	if r.cmd != "" {
		text += "  → " + r.cmd
	}
	return paint(icon + " " + text)
}

// cliRow reports the CLI's own update status.
//
// The CLI upgrades itself where it can — the binary is replaced in place after
// the same signature and checksum checks a fresh install performs. Where the
// binary is not writable (a root-owned prefix, a shared install), it falls back
// to what this row did for every install before the self-updater existed:
// carry the package manager's command and no run, so the user is told how
// rather than shown a failure.
func cliRow(ctx context.Context, out io.Writer) compRow {
	current := firstNonEmpty(buildinfo.Version, "unknown")
	r := compRow{name: "driverforge CLI", current: current, latest: "—"}
	latest := updatecheck.LatestCLI(ctx)
	if latest == "" {
		r.state, r.note = stateUnknown, "couldn't check"
		return r
	}
	r.latest = latest
	if !updatecheck.IsNewer(latest, buildinfo.Version) {
		r.state, r.note = stateUpToDate, "up to date"
		return r
	}

	r.state, r.note = stateUpdate, "update available"

	target, locateErr := selfupdate.Locate()
	var selfUpgradable bool
	r.cmd, selfUpgradable = upgradeAction(target, locateErr)
	if selfUpgradable {
		r.run = func(ctx context.Context) error { return runSelfUpdate(ctx, out, target) }
	}
	return r
}

// upgradeAction decides what an update-available CLI row offers.
//
// The command is carried whether or not the CLI can replace its own binary.
// Only the interactive picker can invoke a row's run, so a piped, CI or no-TTY
// caller sees the rendered table and nothing else — a row without a command
// would tell that caller an update exists and give it no way to take one.
func upgradeAction(target selfupdate.Target, locateErr error) (cmd string, selfUpgradable bool) {
	return updatecheck.UpgradeHint(), locateErr == nil && target.Writable
}

// runSelfUpdate replaces this binary with the latest release.
//
// Every failure here is anticipated — an unreachable store, a release that
// refuses verification, a directory that turned out not to be writable after
// all — so each becomes a UserError carrying the package manager's command as
// the way forward. A failed self-update must leave the user with a working CLI
// and a next step, never a crash card.
func runSelfUpdate(ctx context.Context, out io.Writer, target selfupdate.Target) error {
	installed, err := selfupdate.Apply(ctx, updatecheck.ManifestURL(), target, func(step string) {
		ui.Fprintln(out, ui.Muted("  "+step))
	})
	if err != nil {
		return clierr.WrapT(err, "Couldn't upgrade the CLI",
			"The CLI was left as it was — nothing was replaced.",
			"Upgrade with: "+updatecheck.UpgradeHint())
	}
	ui.Fprintln(out, "\n"+ui.InfoBlock(
		fmt.Sprintf("driverforge CLI upgraded to %s", installed),
		"Replaced "+target.Path,
		"The new version is used by your next command."))
	return nil
}

// agentRow reports the Anvil Agent status for the selected controller. Best-effort:
// no controller, an unreachable one, or a sign-in hiccup each become a visible
// status rather than an error.
func agentRow(ctx context.Context, out io.Writer) compRow {
	r := compRow{name: "Anvil Agent", current: "—", latest: "—"}
	dev, err := orgctx.CurrentDevice()
	if err != nil {
		r.state, r.note = stateNA, "no controller selected"
		return r
	}
	hctx, cancel := context.WithTimeout(ctx, versionHealthTimeout)
	defer cancel()
	_, h, err := dialAgent(hctx, dev)
	if err != nil {
		r.state, r.note = stateUnknown, "couldn't check — "+checkReason(err)
		return r
	}
	r.detail = controllerLabel(dev, h)
	r.current = firstNonEmpty(h.AgentVersion, "unknown")
	if h.AgentFilename == "" {
		r.state, r.note = stateNA, "update in Composer Pro to enable self-upgrade"
		return r
	}
	// Fresh budget for the release-store call: a slow-but-alive controller must
	// not eat the whole window and make the store look unreachable.
	rctx, rcancel := context.WithTimeout(ctx, versionHealthTimeout)
	defer rcancel()
	rel, err := agentrelease.Latest(rctx, h.AgentFilename)
	if err != nil {
		r.state, r.note = stateUnknown, "couldn't reach the release store"
		return r
	}
	r.latest = rel.Version
	if !updatecheck.IsNewer(rel.Version, h.AgentVersion) {
		r.state, r.note = stateUpToDate, "up to date"
		return r
	}
	r.state, r.note, r.cmd = stateUpdate, "update available", "driverforge upgrade"
	target := dev
	r.run = func(c context.Context) error { return runAgentUpgrade(c, out, target) }
	return r
}

// sdkRow reports the embedded Driverforge SDK status for the driver in the working
// directory. Outside a driver, or one not set up for Driverforge, it's not applicable.
func sdkRow(ctx context.Context, out io.Writer) compRow {
	r := compRow{name: "Driverforge SDK", current: "—", latest: "—"}
	proj, err := project.Locate(flagProjectDir)
	if err != nil {
		r.state, r.note = stateNA, "not in a driver project"
		return r
	}
	cfg, err := driverforgeconfig.Load(proj.Dir)
	if err != nil || cfg == nil {
		// A driver project that was never set up: init's job, not upgrade's.
		r.state, r.note = stateNA, "not set up — run `driverforge init`"
		return r
	}
	// The config records which version init embedded; it is not evidence that the
	// setup is still intact. Diagnose the tree — claiming "up to date" about a
	// file we never opened is the one answer this row must never give.
	//
	// A drifted project is NOT offered an inline fix. Restoring it rewrites the
	// entry Lua, the manifest and the squishy, and the picker's contract is that
	// selecting a row IS the consent — it shows no diff and asks nothing about
	// the change itself (only whether to branch for it). That is fine for
	// swapping one vendored file and wrong for patching source, so this row
	// routes to `doctor`, which reviews before it writes.
	if diag := initdriver.Diagnose(proj, len(cfg.BuildTargets) > 0); !diag.Healthy() {
		r.current = "incomplete"
		if len(diag.Failed()) == 1 && diag.Failed()[0].Name == "embedded SDK" {
			r.current = "missing"
		}
		r.state, r.note = stateUnknown, "setup incomplete — run `driverforge doctor`"
		return r
	}
	r.current = firstNonEmpty(cfg.SDKVersion, "unknown")

	rctx, cancel := context.WithTimeout(ctx, versionHealthTimeout)
	defer cancel()
	rel, err := sdkrelease.Resolve(rctx, "latest")
	if err != nil {
		r.state, r.note = stateUnknown, "couldn't reach the release store"
		return r
	}
	r.latest = rel.Version
	if !updatecheck.IsNewer(rel.Version, cfg.SDKVersion) {
		r.state, r.note = stateUpToDate, "up to date"
		return r
	}
	return sdkUpdateRow(ctx, out, r, proj, cfg, rel.Version)
}

// sdkUpdateRow completes an SDK row whose setup is intact and whose version is
// behind latest — exactly the case runSDKUpdate was built for: swap the bundle,
// bump config. The version pair and release notes are shown either way; the
// row is selectable only when the project's git working tree is clean, since
// runSDKUpdate refuses a dirty one. Checking here, when the row is built, means
// the picker never offers an SDK upgrade that would fail after the CLI and
// agent upgrades chosen alongside it have already run.
func sdkUpdateRow(ctx context.Context, out io.Writer, r compRow, proj project.Project, cfg *driverforgeconfig.Config, latest string) compRow {
	r.latest = latest
	r.notesURL = fmt.Sprintf(sdkReleaseNotesURL, latest)
	if !treeClean(ctx, proj.Dir) {
		r.state, r.note = stateBlocked, "update available — "+sdkDirtyTreeNote
		return r
	}
	r.state, r.note, r.cmd = stateUpdate, "update available", "driverforge upgrade"
	p, c, target := proj, cfg, latest
	r.run = func(ctx context.Context) error { return runSDKUpdate(ctx, out, p, c, target) }
	return r
}

// checkReason gives a short reason for a failed component check: a UserError is an
// operational condition we can name (a sign-in issue, after Fix A), anything else
// is treated as the component being unreachable.
func checkReason(err error) string {
	if errors.Is(err, agent.ErrFingerprintMismatch) {
		return "TLS certificate mismatch"
	}
	if clierr.IsUser(err) {
		return "sign-in issue"
	}
	return "unreachable"
}

// upgradeOut is what upgrade reports in structured output. It never upgrades
// anything there: the upgrades act on a connected controller or the working
// tree, and each component's command says how to take it.
type upgradeOut struct {
	Components []componentOut `json:"components"`
}

// componentOut is one component's update status: up_to_date,
// update_available, blocked (available, but can't be applied yet — the note
// says why), unknown (couldn't check) or not_applicable.
type componentOut struct {
	Name     string  `json:"name"`
	Detail   *string `json:"detail"`
	Current  *string `json:"current"`
	Latest   *string `json:"latest"`
	Status   string  `json:"status"`
	Note     string  `json:"note"`
	Command  *string `json:"command"`
	NotesURL *string `json:"release_notes_url"`
}

var compStatus = map[compState]string{
	stateUpToDate: "up_to_date",
	stateUpdate:   "update_available",
	stateUnknown:  "unknown",
	stateNA:       "not_applicable",
	stateBlocked:  "blocked",
}

func upgradeReport(rows []compRow) upgradeOut {
	o := upgradeOut{Components: make([]componentOut, len(rows))}
	for i, r := range rows {
		o.Components[i] = componentOut{
			Name: r.name, Detail: optional(r.detail),
			Current: optional(versionValue(r.current)), Latest: optional(versionValue(r.latest)),
			Status: compStatus[r.state], Note: r.note,
			Command: optional(r.cmd), NotesURL: optional(r.notesURL),
		}
	}
	return o
}

// versionValue drops the table's "—" placeholder for a version not known.
func versionValue(v string) string {
	if v == "—" {
		return ""
	}
	return v
}
