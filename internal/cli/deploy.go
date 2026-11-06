package cli

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/pkg/c4z"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

// newDeployCmd builds the driver, then deploys it to a selected device
// (controller) — the full publish + reload path. Distinct from `driverforge publish`
// (marketplace) and from `driverforge sync` (fast Lua hot-swap).
func newDeployCmd() *cobra.Command {
	var (
		configuration string
		deviceFlag    string
		selectFlag    bool
		refreshFlag   bool
	)
	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Build and deploy a driver to a device (full reload)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Deploy builds with the next version per the project's scheme —
			// Director won't reload a driver whose <version> hasn't increased.
			if !ui.Interactive(flagNoTUI) && !structured() {
				res, err := buildForDeploy(cmd, configuration)
				if err != nil {
					return err
				}
				return runDeploy(cmd.Context(), cmd.OutOrStdout(), res.C4zPath, res.ArtifactName, deviceFlag, selectFlag, refreshFlag)
			}
			return runDeployFramed(cmd, configuration, deviceFlag, selectFlag, refreshFlag)
		},
	}
	cmd.Flags().StringVarP(&configuration, "configuration", "c", "", "build configuration to build and deploy")
	cmd.Flags().StringVar(&deviceFlag, "device", "", "device to deploy to (name, hostname, or id)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "re-select the target device before deploying")
	cmd.Flags().BoolVar(&refreshFlag, "refresh-navigators", false, "refresh navigators even when driver.xml didn't change (they refresh automatically when it did)")
	return cmd
}

// shouldRefreshNavigators decides whether a confirmed deploy warrants a
// navigator refresh: navigators cache the driver's UI schema, so they need a
// kick whenever driver.xml structurally changed since the device's previous
// deploy (prevHash is the deploycache baseline read BEFORE it is overwritten;
// "" means no baseline — a first deploy, which always shows a new UI). The
// force flag refreshes regardless.
func shouldRefreshNavigators(force bool, prevHash, newHash string) bool {
	return force || prevHash == "" || prevHash != newHash
}

// runDeploy resolves the target device and does a full deploy via the agent —
// the plain (non-interactive) path. Shared by `driverforge deploy` and sync's
// escalation to a full deploy; the interactive path runs under a run frame
// instead (runDeployFramed).
func runDeploy(ctx context.Context, out io.Writer, c4zPath, artifactName, pref string, forceSelect, forceRefresh bool) error {
	dev, _, err := headedTarget(ctx, out, "Deploying "+artifactName+" to", pref, forceSelect, resolveDeployDevice)
	if err != nil {
		return err
	}
	return runDeployToDevice(ctx, out, dev, c4zPath, artifactName, forceRefresh)
}

// headedTarget resolves the device a plain deploy or sync ships to, printing
// the heading that titles the operation above the resolve/connect steps that
// follow. It leads with the remembered controller when there is one;
// otherwise the heading prints once resolve decides. early reports whether the
// heading came before resolution, so it isn't followed by another gap.
func headedTarget(ctx context.Context, out io.Writer, action, pref string, forceSelect bool,
	resolve func(context.Context, io.Writer, string, bool) (orgctx.Device, error)) (dev orgctx.Device, early bool, err error) {
	if pref == "" && !forceSelect {
		if cur, cerr := orgctx.CurrentDevice(); cerr == nil {
			ui.Fprintln(out, "\n"+ui.DeployHeading(action, cur.Name))
			early = true
		}
	}
	if dev, err = resolve(ctx, out, pref, forceSelect); err != nil {
		return dev, early, err
	}
	if !early {
		ui.Fprintln(out, "\n"+ui.DeployHeading(action, dev.Name))
	}
	return dev, early, nil
}

// runDeployToDevice is runDeploy after device resolution: connect, stream the
// deploy, record the baseline, and refresh navigators when warranted.
func runDeployToDevice(ctx context.Context, out io.Writer, dev orgctx.Device, c4zPath, artifactName string, forceRefresh bool) error {
	filename := artifactName + ".c4z"
	hash, err := structuralHashOfC4z(c4zPath)
	if err != nil {
		return err
	}

	// Connecting resolves the pinned fingerprint and runs a controller health
	// check — a second or two of silent network work. Show a spinner so the gap
	// between the build card and the deploy progress doesn't look like the process
	// stalled.
	var (
		cl *agent.Client
		h  agent.Health
	)
	werr := ui.WaitForStep(ctx, out, ui.Interactive(flagNoTUI),
		fmt.Sprintf("Connecting to %s…", dev.Name),
		func() (string, error) {
			var herr error
			cl, h, herr = dialAgent(ctx, dev)
			if herr != nil {
				return "", herr
			}
			return connectedLabel(dev, h), nil
		})
	if werr != nil {
		// A connect failure (dialAgent's error) or a Ctrl-C during connect. Both
		// are anticipated conditions; agentUnreachableErr surfaces a UserError
		// (cert changed / Cancelled) as-is and wraps a raw transport error.
		return agentUnreachableErr(dev, werr)
	}
	if !h.Supports("deploy") {
		return agentMissingCapabilityErr(dev, "deploy", h.AgentVersion)
	}

	f, err := os.Open(c4zPath)
	if err != nil {
		return err
	}
	defer f.Close()

	// Bound the whole streamed deploy so a hung agent can't wedge the CLI; a real
	// driver restart can take a while, so the window is generous.
	dctx, cancel := context.WithTimeout(ctx, deployStreamTimeout)
	defer cancel()

	optIn := h.Supports(agent.CapabilityDeployStream) // stream only when advertised
	localVersion := driverVersionFromC4z(c4zPath)

	state, err := runDeployPlain(dctx, out, cl, dev, filename, hash, f, optIn, h.AgentVersion, localVersion)
	if err != nil {
		// First deploy: the driver isn't in the director's project yet. If the
		// agent can bootstrap and we're interactive, offer the install wizard
		// instead of failing. (Uses the parent ctx, not the deploy timeout — the
		// wizard waits on the user.)
		if ui.Interactive(flagNoTUI) && agent.Code(err) == agent.CodeNotInProject && h.Supports(agent.CapabilityBootstrap) {
			return runBootstrap(ctx, out, cl, dev, c4zPath, filename, hash, localVersion)
		}
		return err
	}
	// Record the sync baseline only when the restart was CONFIRMED — an
	// unconfirmed deploy means the old (structurally different) driver may still
	// be running, and caching this hash would let a later `driverforge sync` hot-swap
	// into it, the exact hazard the baseline guards against.
	if deployConfirmed(state) {
		recordBaselineAndRefresh(ctx, deployedDriver{
			Client: cl, Health: h, Device: dev,
			Filename: filename, Hash: hash, Force: forceRefresh,
			Report: func(format string, a ...any) { fmt.Fprintf(out, "  "+format+"\n", a...) },
			Warn:   func(format string, a ...any) { fmt.Fprintf(out, "  ⚠ "+format+"\n", a...) },
		})
	}
	return nil
}

// deployConfirmed reports whether a terminal deploy state means the new driver
// is verifiably running (so its structural hash is a valid sync baseline).
// StateUnconfirmed — the agent couldn't confirm the restart — is deliberately
// excluded.
func deployConfirmed(state agent.DeployState) bool {
	return state == agent.StateConfirmed || state == agent.StateConfirmedWithErrors
}

const (
	// deployStreamTimeout bounds the whole streamed deploy (upload + install +
	// restart loop). Generous: a real restart can take many seconds.
	deployStreamTimeout = 3 * time.Minute
	// navigatorRefreshTimeout bounds the post-deploy navigator refresh — a
	// single fire-and-forget POST that must never hold a finished deploy
	// hostage.
	navigatorRefreshTimeout = 5 * time.Second
	// On a self-deploy the agent restarts itself and can't report its own
	// readiness, so the CLI polls /healthz for the version to flip.
	selfDeployPollTimeout  = 90 * time.Second
	selfDeployPollInterval = time.Second
)

// runDeployFramed is the interactive deploy: the version bump and (only when
// the picker is needed) the device choice happen up front, then the whole
// build + find + connect + deploy runs under a single in-place run frame.
func runDeployFramed(cmd *cobra.Command, configuration, pref string, forceSelect, forceRefresh bool) error {
	ctx := cmd.Context()
	out := textOut(cmd)
	opts, err := deployBuildOptions(configuration)
	if err != nil {
		return err
	}
	target, resolve, err := frameDeviceResolution(ctx, out, pref, forceSelect)
	if err != nil {
		return err
	}
	defer metricsClient.Flush(ctx)
	shipped, err := deployFramedTo(ctx, out, opts, configuration, target, resolve, forceRefresh)
	if err != nil {
		return err
	}
	return emit(cmd, shipped, noText)
}

// shipOut is what deploy and sync report in structured output.
type shipOut struct {
	// Action is what reached the controller: "deploy" (a full reload) or
	// "sync" (a Lua hot-swap). Reason says why a sync became a deploy.
	Action string       `json:"action"`
	Reason *string      `json:"reason"`
	Build  buildOut     `json:"build"`
	Device deviceRefOut `json:"device"`
	// State is the deploy's outcome — confirmed, confirmed_with_errors or
	// unconfirmed — or "synced".
	State     string  `json:"state"`
	Version   *string `json:"version"`
	ReportURL *string `json:"report_url"`
	// Instances are the driver instances a sync hot-swapped; Verified is
	// whether it was checked against the last deploy's driver.xml.
	Instances []string  `json:"instances,omitempty"`
	Verified  *bool     `json:"verified,omitempty"`
	Warnings  []warning `json:"warnings"`
}

// deployFramedTo runs the full build + deploy under one run frame, resolving
// the target device inside it via resolve (target names the frame's heading
// before resolution). Shared by `driverforge deploy` and sync's escalation to
// a full deploy (which reuses the device sync already resolved). When the
// driver isn't in the director's project yet and the agent can bootstrap, the
// frame ends amber and the first-install wizard runs after it.
func deployFramedTo(ctx context.Context, out io.Writer, opts c4z.Options, configuration, target string, resolve deviceResolver, forceRefresh bool) (shipOut, error) {
	// The wizard runs after the frame with the command's context — the frame's
	// work context is cancelled on teardown, and the wizard waits on the user.
	var bootstrap func(ctx context.Context) error
	shipped := shipOut{Action: "deploy"}
	warnings, err := runFrame(ctx, out, ui.FrameOptions{
		Step:        "Deploying to " + target + "…",
		DetailsOpen: flagVerbose,
	}, func(ctx context.Context, f ui.Progress) error {
		res, err := buildInFrame(ctx, f, opts, configuration)
		if err != nil {
			return err
		}
		shipped.Build = buildOutOf(res, configuration)
		filename := res.ArtifactName + ".c4z"
		hash, err := structuralHashOfC4z(res.C4zPath)
		if err != nil {
			return err
		}

		dev, err := resolve(ctx, f)
		if err != nil {
			return err
		}
		f.Step("Connecting to " + dev.Name + "…")
		cl, h, herr := dialAgent(ctx, dev)
		if herr != nil {
			return agentUnreachableErr(dev, herr)
		}
		f.Logf("✓ %s", connectedLabel(dev, h))
		f.Row("controller", dev.Name)
		shipped.Device = deviceRefOf(dev, h)
		if !h.Supports("deploy") {
			return agentMissingCapabilityErr(dev, "deploy", h.AgentVersion)
		}

		body, err := os.Open(res.C4zPath)
		if err != nil {
			return err
		}
		defer body.Close()

		// Bound the whole streamed deploy so a hung agent can't wedge the CLI; a
		// real driver restart can take a while, so the window is generous.
		dctx, cancel := context.WithTimeout(ctx, deployStreamTimeout)
		defer cancel()

		optIn := h.Supports(agent.CapabilityDeployStream) // stream only when advertised
		localVersion := driverVersionFromC4z(res.C4zPath)
		terminal, err := deployStreamInFrame(dctx, f, cl, dev, filename, hash, body, optIn, h.AgentVersion, localVersion)
		if err != nil {
			// First deploy: the driver isn't in the director's project yet. If the
			// agent can bootstrap, end the frame amber and run the install wizard
			// after it (the wizard drives its own prompts and waits on the user, so
			// it uses the parent ctx, not the deploy timeout). Structured output
			// has nobody to answer the wizard, so it reports the condition.
			if !structured() && agent.Code(err) == agent.CodeNotInProject && h.Supports(agent.CapabilityBootstrap) {
				f.Warnf("the driver isn't in the project on %s yet", dev.Name)
				f.Step("First-install setup required")
				bootstrap = func(ctx context.Context) error {
					return runBootstrap(ctx, out, cl, dev, res.C4zPath, filename, hash, localVersion)
				}
				return nil
			}
			return err
		}
		shipped.State, shipped.Version, shipped.ReportURL = string(terminal.State), optional(terminal.Version), optional(terminal.ReportURL)
		if deployConfirmed(terminal.State) {
			recordBaselineAndRefresh(ctx, deployedDriver{
				Client: cl, Health: h, Device: dev,
				Filename: filename, Hash: hash, Force: forceRefresh,
				Report: f.Logf, Warn: f.Warnf,
			})
		}
		return nil
	})
	shipped.Warnings = nonNilWarnings(warnings)
	if err != nil {
		return shipped, err
	}
	if bootstrap != nil {
		return shipped, bootstrap(ctx)
	}
	return shipped, nil
}

// deployStreamInFrame streams the deploy reporting progress into the frame.
// The spinner line always carries a present-tense "what's happening now" label
// derived from the deploy state — the agent's Step strings describe the state
// just reached ("driver written to controller") and belong in the log, not on
// a spinner. The terminal event maps to the same outcomes the plain path uses
// — a failure error, an amber warning for confirmed-with-errors / unconfirmed,
// or the deployed-version success label. It returns the terminal event, its
// Version resolved to the version now running.
func deployStreamInFrame(ctx context.Context, f ui.Progress, cl *agent.Client, dev orgctx.Device, filename, hash string, body io.Reader, optIn bool, oldAgentVersion, localVersion string) (agent.DeployEvent, error) {
	f.Step("Uploading the driver…")
	var lastStep string
	terminal, sErr := streamDeploy(ctx, cl, dev, filename, hash, "", body, optIn, oldAgentVersion, func(e agent.DeployEvent) {
		if e.Terminal {
			return
		}
		lastStep = e.Step
		f.Logf("→ %s", e.Step)
		f.Step(deployStateLabel(e))
	})
	if sErr != nil {
		return agent.DeployEvent{}, friendlyDeployErr(dev, sErr)
	}
	// The terminal event's Step carries the resolution text for the in-flight
	// step (mirroring DeployModel.resolveLast).
	resolved := firstNonEmpty(terminal.Step, lastStep)
	version := firstNonEmpty(terminal.Version, localVersion)
	terminal.Version = version
	switch terminal.State {
	case agent.StateFailed:
		if resolved != "" {
			f.Logf("✗ %s", resolved)
		}
		return terminal, deployFailErr(terminal)
	case agent.StateConfirmedWithErrors:
		if resolved != "" {
			f.Logf("✓ %s", resolved)
		}
		f.Warnf("%s", firstNonEmpty(terminal.Error, "the driver's init handler reported an error"))
		if terminal.ReportURL != "" {
			f.Logf("→ %s", terminal.ReportURL)
		}
		f.Row("version", version)
		f.Step(deployedLabel(version))
		return terminal, nil
	case agent.StateUnconfirmed:
		if resolved != "" {
			f.Logf("⚠ %s", resolved)
		}
		f.Warnf("%s", firstNonEmpty(terminal.Hint, "couldn't confirm the restart — check the controller"))
		f.Step("Deployed (unconfirmed)")
		return terminal, nil
	default: // confirmed (and any unexpected terminal) → success
		if resolved != "" {
			f.Logf("✓ %s", resolved)
		}
		f.Row("version", version)
		f.Step(deployedLabel(version))
		return terminal, nil
	}
}

// deployStateLabel maps a non-terminal deploy state to the present-tense
// in-progress label the frame's spinner line shows while that state is
// current. Branching is on State (the contract), never on Step (a display
// string); an unrecognised state falls back to the agent's wording.
func deployStateLabel(e agent.DeployEvent) string {
	switch e.State {
	case agent.StateStaged:
		// The driver's bytes are on the controller; Director's reinstall is next.
		return "Waiting for Director…"
	case agent.StateReinstalling:
		return "Reinstalling the driver…"
	case agent.StateRestarting:
		return "Restarting the driver…"
	default:
		return e.Step
	}
}

// deployedLabel is the frame's resolved step line for a completed deploy.
func deployedLabel(version string) string {
	if version == "" {
		return "Deployed"
	}
	return "Deployed version " + version
}

// The constructor is replaceable so the real event loop can run with headless
// input/output in tests, without requiring a controlling terminal.
var newDeployProgram = tea.NewProgram

// runDeployProgram drives a DeployModel Bubble Tea program over a streamed
// deploy-shaped operation: start launches the stream (reporting progress via
// send) and returns its terminal event. Shared by deploy, agent upgrade, and
// the bootstrap install. It returns the terminal deploy state (for baseline
// decisions) alongside any error.
func runDeployProgram(ctx context.Context, dev orgctx.Device, action, localVersion string, start func(send func(agent.DeployEvent)) (agent.DeployEvent, error)) (agent.DeployState, error) {
	m := ui.NewDeployModel(action, dev.Name)
	p := newDeployProgram(m, tea.WithContext(ctx))
	go func() {
		terminal, sErr := start(func(e agent.DeployEvent) { p.Send(ui.DeployEventMsg(e)) })
		p.Send(deployDoneMsg(dev, terminal, sErr, localVersion))
	}()
	if _, err := p.Run(); err != nil {
		return "", classifyDeployRunErr(dev, err)
	}
	if m.Outcome() == ui.DeployFail {
		// m.Err() is already classified (deployDoneMsg applies
		// friendlyDeployErr); this maps a ctrl+c interrupt to the friendly
		// Cancelled and passes everything else through.
		return m.TermState(), classifyDeployRunErr(dev, m.Err())
	}
	return m.TermState(), nil
}

// runDeployPlain streams the deploy as plain lines for non-interactive output.
// It returns the terminal deploy state (for baseline decisions) alongside any
// error.
func runDeployPlain(ctx context.Context, out io.Writer, cl *agent.Client, dev orgctx.Device, filename, hash string, body io.Reader, optIn bool, oldAgentVersion, localVersion string) (agent.DeployState, error) {
	fmt.Fprintf(out, "\nDeploying %s to %s…\n", filename, dev.Name)
	terminal, sErr := streamDeploy(ctx, cl, dev, filename, hash, "", body, optIn, oldAgentVersion, ui.DeployPlain(out))
	if sErr != nil {
		return "", friendlyDeployErr(dev, sErr)
	}
	version := firstNonEmpty(terminal.Version, localVersion)
	switch terminal.State {
	case agent.StateFailed:
		return terminal.State, deployFailErr(terminal)
	case agent.StateConfirmedWithErrors:
		fmt.Fprintf(out, "  ⚠ %s\n", firstNonEmpty(terminal.Error, "the driver's init handler reported an error"))
		if terminal.ReportURL != "" {
			fmt.Fprintf(out, "    → %s\n", terminal.ReportURL)
		}
		ui.PrintDeploySummaryPlain(out, version)
	case agent.StateUnconfirmed:
		fmt.Fprintf(out, "  ⚠ %s\n", firstNonEmpty(terminal.Hint, "couldn't confirm the restart — check the controller"))
	default:
		ui.PrintDeploySummaryPlain(out, version)
	}
	return terminal.State, nil
}

// streamDeploy runs the streamed deploy and, for a self-deploy (the agent
// reinstalls itself and can't observe its own restart), resolves the terminal by
// polling GET /healthz until the agent's reported version changes.
func streamDeploy(ctx context.Context, cl *agent.Client, dev orgctx.Device, filename, hash, sha string, body io.Reader, optIn bool, oldAgentVersion string, onEvent func(agent.DeployEvent)) (agent.DeployEvent, error) {
	_, terminal, err := cl.DeployStream(ctx, filename, hash, sha, body, optIn, onEvent)
	if err != nil {
		return agent.DeployEvent{}, err
	}
	if terminal.Reason == agent.ReasonSelfDeploy {
		if onEvent != nil { // DeployStream documents onEvent may be nil
			onEvent(agent.DeployEvent{State: agent.StateRestarting, Step: "agent restarting (self-deploy)"})
		}
		terminal = resolveSelfDeploy(ctx, cl, dev, oldAgentVersion)
	}
	return terminal, nil
}

// resolveSelfDeploy polls /healthz until the agent's version flips (the deployed
// build came up) or the poll window elapses.
func resolveSelfDeploy(ctx context.Context, cl *agent.Client, dev orgctx.Device, oldAgentVersion string) agent.DeployEvent {
	ctx, cancel := context.WithTimeout(ctx, selfDeployPollTimeout)
	defer cancel()
	certificateMismatch := false
	for {
		var h agent.Health
		var err error
		cl, h, err = healthWithPinRecovery(ctx, cl, dev)
		if err == nil {
			certificateMismatch = false
		} else if errors.Is(err, agent.ErrKeyMismatch) {
			certificateMismatch = true
		}
		if err == nil && h.AgentVersion != "" && h.AgentVersion != oldAgentVersion {
			return agent.DeployEvent{State: agent.StateConfirmed, Step: "agent restarted", Version: h.AgentVersion, Terminal: true}
		}
		if ctx.Err() != nil {
			if certificateMismatch {
				return agent.DeployEvent{State: agent.StateUnconfirmed, Step: "agent certificate could not be verified",
					Hint: "the driver was written, but the agent's TLS certificate does not match its registered fingerprint — restart could not be confirmed", Terminal: true}
			}
			return agent.DeployEvent{State: agent.StateUnconfirmed, Step: "agent didn't report back in time",
				Hint: "the agent is restarting — re-check shortly", Terminal: true}
		}
		select {
		case <-ctx.Done():
		case <-time.After(selfDeployPollInterval):
		}
	}
}

// deployDoneMsg maps the stream outcome to the TUI's terminal message.
func deployDoneMsg(dev orgctx.Device, terminal agent.DeployEvent, sErr error, localVersion string) ui.DeployDoneMsg {
	if sErr != nil {
		return ui.DeployDoneMsg{Outcome: ui.DeployFail, Err: friendlyDeployErr(dev, sErr)}
	}
	version := firstNonEmpty(terminal.Version, localVersion)
	switch terminal.State {
	case agent.StateFailed:
		return ui.DeployDoneMsg{State: terminal.State, Step: terminal.Step, Outcome: ui.DeployFail, Err: deployFailErr(terminal)}
	case agent.StateConfirmedWithErrors:
		return ui.DeployDoneMsg{State: terminal.State, Step: terminal.Step, Version: version,
			Detail: firstNonEmpty(terminal.Error, "the driver's init handler reported an error"), ReportURL: terminal.ReportURL, Outcome: ui.DeployWarn}
	case agent.StateUnconfirmed:
		return ui.DeployDoneMsg{State: terminal.State, Step: terminal.Step,
			Detail: firstNonEmpty(terminal.Hint, "couldn't confirm the restart — check the controller"), Outcome: ui.DeployWarn}
	default: // confirmed (and any unexpected terminal) → success
		return ui.DeployDoneMsg{State: terminal.State, Step: terminal.Step, Version: version, Outcome: ui.DeployOK}
	}
}

// driverVersionFromC4z reads the version label from the built .c4z's
// driver.xml for the "Version X deployed" line. Best-effort: "" on any failure.
func driverVersionFromC4z(c4zPath string) string {
	xml, err := readC4zEntry(c4zPath, "driver.xml")
	if err != nil {
		return ""
	}
	version, _ := driverxml.ReadVersion(xml)
	return version
}

// dialAgent builds a deploy-server client for dev and confirms it with a health
// probe, self-healing a stale key pin. A reissued certificate keeps its key, so
// the pin only goes stale when the agent is re-enrolled with a new key (a reset
// controller, or a reinstall that lost its key). Then it re-fetches the pin from
// the graph (where the agent registers its new key), persists it, and retries
// once. Callers get the (possibly rebuilt)
// client and its health, so later requests reuse the healed connection.
func dialAgent(ctx context.Context, dev orgctx.Device) (*agent.Client, agent.Health, error) {
	cl, err := agentForDevice(ctx, dev)
	if err != nil {
		return nil, agent.Health{}, err
	}
	return healthWithPinRecovery(ctx, cl, dev)
}

// Only authenticated graph data can replace a pin. Keep the original client on
// a failed refresh, and persist the replacement only after it verifies health.
func healthWithPinRecovery(ctx context.Context, cl *agent.Client, dev orgctx.Device) (*agent.Client, agent.Health, error) {
	h, herr := cl.Health(ctx)
	if herr == nil || !errors.Is(herr, agent.ErrKeyMismatch) || dev.DeviceID == "" {
		return cl, h, herr
	}
	pin, ferr := driverforgeapi.New().DeviceKeyPin(ctx, dev.DeviceID)
	if ferr != nil || pin == "" || pin == dev.AgentKeyPin {
		return cl, h, herr
	}
	addr, ok := agentAddr(dev)
	if !ok {
		return cl, h, herr
	}
	refreshed := agent.New("https://"+addr, pin)
	h, err := refreshed.Health(ctx)
	if err != nil {
		return cl, h, herr
	}
	persistKeyPin(dev, pin)
	return refreshed, h, nil
}

// connectedLabel is the ✓ line shown once the agent answers — filling the gap
// between the build card and the deploy progress with something concrete.
func connectedLabel(dev orgctx.Device, h agent.Health) string {
	if h.AgentVersion != "" {
		return fmt.Sprintf("Connected to %s (Anvil Agent %s)", dev.Name, h.AgentVersion)
	}
	return "Connected to " + dev.Name
}

// agentForDevice builds a deploy-server client for the device's address.
func agentForDevice(ctx context.Context, dev orgctx.Device) (*agent.Client, error) {
	addr, ok := agentAddr(dev)
	if !ok {
		return nil, clierr.WithCode(clierr.User(
			fmt.Sprintf("no network address on record for %s", dev.Name),
			"re-select it with `driverforge device select`"), codeAgentUnreachable)
	}

	// Resolve the agent's key pin: cached, else fetch by hardware id (e.g. the
	// agent registered after selection) and persist it so later
	// deploy/sync/upgrade/health probes don't repeat the graph round-trip.
	pin := dev.AgentKeyPin
	if pin == "" && dev.DeviceID != "" {
		if v, err := driverforgeapi.New().DeviceKeyPin(ctx, dev.DeviceID); err == nil && v != "" {
			pin = v
			persistKeyPin(dev, pin)
		}
	}
	if pin == "" && os.Getenv("DRIVERFORGE_AGENT_INSECURE") == "" {
		return nil, clierr.WithCode(clierr.User(
			fmt.Sprintf("no agent key on record for %s — the controller may not have finished registration", dev.Name),
			"re-run `driverforge device select` once it's active (or set DRIVERFORGE_AGENT_INSECURE=1 for local testing)"), codeCertificateChanged)
	}

	return agent.New("https://"+addr, pin), nil
}

// persistKeyPin caches a freshly-fetched agent key pin back onto the selected
// device, but only when dev IS the current device — so a probe against a
// --device-specified other controller never overwrites the wrong record.
// Best-effort: a failed save just means the next call re-fetches.
func persistKeyPin(dev orgctx.Device, pin string) {
	cur, err := orgctx.CurrentDevice()
	if err != nil || cur.ID != dev.ID {
		return
	}
	if cur.AgentKeyPin == pin {
		return
	}
	cur.AgentKeyPin = pin
	_ = orgctx.SetCurrentDevice(cur)
}

// deviceHost is the controller's connectable host. It prefers the IP — a Control4
// hostname ("core1-<MAC>") isn't a resolvable DNS name, so dialing it fails,
// especially across a VLAN.
func deviceHost(dev orgctx.Device) string {
	if dev.IP != "" {
		return dev.IP
	}
	return dev.Hostname
}

// agentAddr is the controller's deploy-server host:port (ok is false when no
// address is on record).
func agentAddr(dev orgctx.Device) (addr string, ok bool) {
	host := deviceHost(dev)
	if host == "" {
		return "", false
	}
	return net.JoinHostPort(host, agentPortInUse()), true
}

// structuralHashOfC4z reads driver.xml from the built .c4z and hashes it.
func structuralHashOfC4z(c4zPath string) (string, error) {
	xml, err := readC4zEntry(c4zPath, "driver.xml")
	if err != nil {
		return "", err
	}
	return driverxml.StructuralHash(xml)
}

// readC4zEntry reads a single named entry from a .c4z (zip) into memory.
func readC4zEntry(c4zPath, name string) ([]byte, error) {
	r, err := zip.OpenReader(c4zPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, f := range r.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("%s not found in %s", name, filepath.Base(c4zPath))
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
