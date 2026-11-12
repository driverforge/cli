package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/buildinfo"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeconfig"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/project"
	"github.com/driverforge/cli/internal/remote4"
	"github.com/driverforge/cli/internal/sdkrelease"
	"github.com/driverforge/cli/internal/ui"
)

// newRunCmd runs the driver locally through Remote4: the driver's Lua runs in
// a local process while a stub on the controller forwards every callback to
// it and executes its C4 calls. See docs/remote4-protocol.md in the platform repo.
func newRunCmd() *cobra.Command {
	var o runOptions
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the driver locally against a real controller",
		Long: `Run your driver's Lua on this machine while a real controller drives it,
with no build or deploy between changes: save a file, trigger the action
again, and the new code handles it.

A stub driver replaces yours on the controller while this runs. It forwards
every callback (ReceivedFromProxy, OnPropertyChanged, OnTimerExpired, …) to a
local Lua process running your source, and executes the C4 API calls your
code makes against the real controller. To step through the code, pass
--debug and connect a Lua debugger (ZeroBrane Studio, or VS Code with a
mobdebug extension) listening on localhost:8172, or --debug=host:port for
another address.

When the controller's agent supports it, the session's traffic goes over your
local network through the agent rather than through the cloud, so each C4 call
costs a LAN round trip. The cloud connection stays up as the fallback.

Ctrl+C stops it; run 'driverforge deploy' to restore your driver, or pass
--deploy-on-exit to have run deploy it for you when it stops.`,
		Args:   cobra.NoArgs,
		Hidden: !experimental("run"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !experimental("run") {
				return clierr.WithCode(clierr.UserT("Not available yet",
					"driverforge run isn't available in this release",
					"it's coming soon"), codeNotAvailable)
			}
			return runLocally(cmd, o)
		},
	}
	cmd.Flags().StringVarP(&o.configuration, "configuration", "c", "", "build configuration to run (selects config.<name>.lua)")
	cmd.Flags().StringVar(&o.device, "device", "", "controller to run against (name, hostname, or id)")
	cmd.Flags().BoolVar(&o.selectDevice, "select", false, "re-select the target controller first")
	cmd.Flags().StringVar(&o.lua, "lua", "", "Lua 5.1 interpreter to run the driver with (default: lua5.1, luajit or lua on PATH)")
	cmd.Flags().StringVar(&o.debugAddr, "debug", "", "connect the local Lua process to a Lua debugger listening at host:port")
	cmd.Flags().Lookup("debug").NoOptDefVal = defaultDebugAddr
	cmd.Flags().BoolVar(&o.noReload, "no-reload", false, "don't reload the driver when source files change")
	cmd.Flags().BoolVar(&o.noRelay, "no-relay", false, "send everything through the cloud, not over the local network through the agent")
	cmd.Flags().BoolVar(&o.deployOnExit, "deploy-on-exit", false, "when it stops, build and deploy the driver to replace the stub, as driverforge deploy would")
	return cmd
}

type runOptions struct {
	configuration string
	device        string
	selectDevice  bool
	lua           string
	debugAddr     string
	noReload      bool
	noRelay       bool
	deployOnExit  bool
}

// defaultDebugAddr is where --debug connects without a value: ZeroBrane
// Studio's default mobdebug port.
const defaultDebugAddr = "localhost:8172"

const (
	runtimeReadyTimeout = 30 * time.Second
	tokenRefreshEvery   = 5 * time.Minute
	runtimeStopTimeout  = 3 * time.Second
)

// runEnv is everything a session needs before it touches the controller.
type runEnv struct {
	proj     project.Project
	cfg      *driverforgeconfig.Config
	userID   string
	token    string
	endpoint remote4.BrokerEndpoint
	dev      orgctx.Device
	session  string
	relayKey string
}

func prepareRun(ctx context.Context, out io.Writer, o runOptions) (runEnv, error) {
	var env runEnv
	proj, err := requireProject(flagProjectDir)
	if err != nil {
		return env, err
	}
	cfg, err := requireInitialised(proj.Dir)
	if err != nil {
		return env, err
	}
	creds, err := auth.Load()
	if err != nil {
		return env, err
	}
	if creds.Subject == "" {
		return env, clierr.WithCode(clierr.User("not signed in", "run `driverforge login` first"), codeAuthRequired)
	}
	token, err := auth.MQTTAccessToken(ctx)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return env, clierr.WithCode(clierr.WrapT(err, "Session expired", "your sign-in has expired, so the driver can't start",
				"run `driverforge login`, then `driverforge run` again"), codeAuthRequired)
		}
		if ctx.Err() != nil || clierr.IsUser(err) {
			return env, err // already says what went wrong — don't flatten it to "unreachable"
		}
		return env, clierr.WithCode(clierr.WrapT(err, "Driverforge unreachable", "couldn't reach Driverforge to start the driver",
			"check your connection and try again"), codeConnectionLost)
	}
	endpoint, err := remote4.ResolveBroker()
	if err != nil {
		return env, err
	}
	dev, err := resolveDeployDevice(ctx, out, o.device, o.selectDevice)
	if err != nil {
		return env, err
	}
	relayKey, err := remote4.NewRelayKey()
	if err != nil {
		return env, err
	}
	return runEnv{proj: proj, cfg: cfg, userID: creds.Subject, token: token, endpoint: endpoint, dev: dev,
		session: newSessionID(), relayKey: relayKey}, nil
}

// buildStub builds the real driver as deploy would, unencrypted, then swaps its
// script for the stub bundle. driver.xml is untouched. It returns the stub
// package's path and the driver's artifact name.
func buildStub(ctx context.Context, cmd *cobra.Command, out io.Writer, o runOptions, env runEnv, handlers []string) (stubPath, artifact string, err error) {
	opts, err := deployBuildOptions(o.configuration)
	if err != nil {
		return "", "", err
	}
	noEncrypt := false
	opts.Encrypt = &noEncrypt
	res, err := executeBuild(cmd, opts, o.configuration)
	if err != nil {
		return "", "", err
	}
	sdk := loadSDKBundle(ctx, out, env.cfg)
	stubCfg := remote4.StubConfig{
		SessionID: env.session, UserID: env.userID, Token: env.token, Handlers: handlers,
		BrokerHost: env.endpoint.Host, BrokerPort: env.endpoint.Port, BrokerTLS: env.endpoint.TLS,
		SkipHostnameCheck: env.endpoint.TLS, CAPEM: string(remote4.BrokerCA()),
		DriverToken: stubDriverToken(ctx, out, env.cfg, sdk != nil), StubVersion: strings.TrimPrefix(buildinfo.String(), "driverforge "),
	}
	if !o.noRelay {
		stubCfg.RelayKey = env.relayKey
	}
	stubPath = filepath.Join(env.proj.DistDir, res.ArtifactName+"-remote4.c4z")
	if err := remote4.RewriteC4z(res.C4zPath, stubPath, remote4.Bundle(stubCfg, sdk)); err != nil {
		return "", "", err
	}
	return stubPath, res.ArtifactName, nil
}

// stubDriverToken is the project token the stub's logs and events ride on. Only
// looked up when the SDK is present to use it.
func stubDriverToken(ctx context.Context, out io.Writer, cfg *driverforgeconfig.Config, haveSDK bool) string {
	if !haveSDK {
		return ""
	}
	t, err := lookupProjectToken(ctx, out, cfg)
	if err != nil {
		ui.Fprintln(out, ui.Warning("no driver token; the stub's logs and events won't reach the Anvil console"))
		return ""
	}
	return t
}

func connectRunBroker(ctx context.Context, out io.Writer, env runEnv) (remote4.Broker, error) {
	broker, err := remote4.Connect(ctx, remote4.BrokerOptions{
		Endpoint: env.endpoint,
		ClientID: env.userID + "|remote4-cli-" + env.session,
		Token:    func() (string, error) { return auth.MQTTAccessToken(context.Background()) },
		OnState: func(s remote4.ConnectionState, cause error) {
			switch s {
			case remote4.StateConnected:
				ui.Fprintln(out, ui.Success("broker connection restored"))
			case remote4.StateLost:
				ui.Fprintln(out, ui.Warning("broker connection lost: "+errString(cause)+" — reconnecting"))
			}
		},
	})
	if err != nil {
		return nil, clierr.WithCode(clierr.Wrap(err, "couldn't connect to the Anvil broker at "+env.endpoint.URL,
			"check your network, or set DRIVERFORGE_MQTT_URL for a local stack"), codeConnectionLost)
	}
	return broker, nil
}

func runLocally(cmd *cobra.Command, o runOptions) error {
	out := textOut(cmd)
	// The session's events are its output: lines for a person, or one JSON
	// object per event on stdout under structured output.
	render := func(e remote4.Event) { renderRunEvent(out, e) }
	if structured() {
		stdout := cmd.OutOrStdout()
		render = func(e remote4.Event) { writeJSON(stdout, runEventOf(e)) }
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := prepareRun(ctx, out, o)
	if err != nil {
		return err
	}
	ui.Fprintln(out, "\n"+ui.DeployHeading("Running "+env.cfg.Project.Name+" locally against", env.dev.Name))
	ui.Fprintln(out, ui.Muted("  session "+env.session))

	// 1. The local runtime first: a driver that doesn't compile never touches
	// the controller. It is not run yet — drivers call C4 at file scope, and
	// only a live stub can answer — so the stub's handler list comes from a
	// static scan of the source.
	handlers, err := remote4.ScanHandlers(env.proj.SrcDir)
	if err != nil {
		return err
	}
	rt, err := remote4.StartRuntime(ctx, remote4.RuntimeOptions{
		Lua: o.lua, SrcDir: env.proj.SrcDir, Entry: env.cfg.Entry, Configuration: o.configuration,
		MobDebug: o.debugAddr, Stderr: out,
	})
	if err != nil {
		return clierr.WithCode(clierr.Wrap(err, "couldn't start the local Lua runtime",
			"install Lua 5.1 (brew install lua@5.1) or point --lua at an interpreter"), codeRuntimeUnavailable)
	}
	defer func() { _ = rt.Shutdown(runtimeStopTimeout) }()
	compiled, err := awaitCompiled(ctx, out, rt)
	if err != nil {
		return err
	}
	ui.Fprintln(out, ui.Success(fmt.Sprintf("Compiled %s locally (%d handlers found)", compiled.Entry, len(handlers))))

	// 2. The stub package.
	stubPath, artifact, err := buildStub(ctx, cmd, out, o, env, handlers)
	if err != nil {
		return err
	}
	defer os.Remove(stubPath)

	// 3. Our end of the broker before the stub's, so no early callback is lost.
	broker, err := connectRunBroker(ctx, out, env)
	if err != nil {
		return err
	}
	transport := remote4.NewTransport(broker, env.userID, env.session, func(s remote4.RelayState, cause error) {
		switch s {
		case remote4.RelayAttached:
			ui.Fprintln(out, ui.Success("Local network relay up through the agent: C4 calls skip the cloud"))
		case remote4.RelayLost:
			ui.Fprintln(out, ui.Warning("local network relay lost ("+errString(cause)+"); back on the cloud broker"))
		}
	})
	defer transport.Close()
	ui.Fprintln(out, ui.Success("Connected to broker "+env.endpoint.Host))

	bridge := remote4.NewBridge(rt, transport, env.userID, env.session)
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridge.Run(ctx) }()

	// 4. Swap the stub in on the controller.
	if err := runDeployToDevice(ctx, out, env.dev, stubPath, artifact, false); err != nil {
		return err
	}

	// 5. Move the session onto the LAN when the agent can relay it. The broker
	// carries everything until then, and again if the relay drops.
	if !o.noRelay {
		go openRunRelay(ctx, out, env, artifact+".c4z", transport)
	}

	// 6. Reload on save, keep the stub's token fresh, render the session.
	if !o.noReload {
		go remote4.Watch(ctx, env.proj.SrcDir, 0, func(changed []string) {
			ui.Fprintln(out, ui.Muted("  changed: "+strings.Join(changed, ", ")))
			_ = bridge.Reload()
		})
	}
	go refreshStubToken(ctx, bridge, env.token)

	ui.Fprintln(out, "\n"+ui.Success("Running. Ctrl+C to stop.")+"\n")
	err = renderRunEvents(ctx, out, render, bridge, bridgeDone, handlers)

	// Teardown in order: tell the stub, stop the runtime, then disconnect.
	endCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = endWithContext(endCtx, bridge)
	if o.deployOnExit {
		return deployOnExit(cmd, out, o, env, err)
	}
	ui.Fprintln(out, "\n"+ui.Muted("Stopped.")+" Run `driverforge deploy` to restore your driver.")
	return err
}

// deployOnExit replaces the stub with the real driver once a run stops: it
// builds and deploys the driver as `driverforge deploy` would, to the
// controller the run used. Only reached once the stub is installed. The run's
// context is already cancelled by then, so the deploy gets its own, which a
// second Ctrl+C still cancels.
func deployOnExit(cmd *cobra.Command, out io.Writer, o runOptions, env runEnv, runErr error) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ui.Fprintln(out, "\n"+ui.Muted("Stopped. Deploying "+env.cfg.Project.Name+" to replace the stub…"))
	res, err := buildForDeploy(cmd, o.configuration)
	if err == nil {
		err = runDeployToDevice(ctx, out, env.dev, res.C4zPath, res.ArtifactName, false)
	}
	if err != nil {
		ui.Fprintln(out, ui.Warning("the stub is still installed on "+env.dev.Name+"; run `driverforge deploy` to restore your driver"))
		if runErr != nil {
			return runErr
		}
		return err
	}
	ui.Fprintln(out, ui.Success("Deployed "+res.ArtifactName+" to "+env.dev.Name+", replacing the stub"))
	return runErr
}

// openRunRelay opens the LAN relay through the controller's agent and hands
// it to the transport. Every failure leaves the session on the broker, which
// already works, so each is a note rather than an error.
func openRunRelay(ctx context.Context, out io.Writer, env runEnv, filename string, transport *remote4.Transport) {
	onBroker := func(why string) {
		ui.Fprintln(out, ui.Muted("  "+why+"; C4 calls go through the cloud broker"))
	}
	cl, h, err := dialAgent(ctx, env.dev)
	if err != nil {
		onBroker("couldn't reach the agent for a local network relay (" + errString(err) + ")")
		return
	}
	if !h.Supports(agent.CapabilityRemote4Relay) {
		onBroker("the agent on " + env.dev.Name + " can't relay over the local network yet (update it with `driverforge upgrade`)")
		return
	}
	conn, err := cl.OpenRemote4Relay(ctx, filename, env.session, env.relayKey)
	if err != nil {
		onBroker("the agent refused a local network relay (" + errString(err) + ")")
		return
	}
	if err := transport.Attach(ctx, conn); err != nil && ctx.Err() == nil {
		onBroker("the local network relay didn't come up (" + errString(err) + ")")
	}
}

// awaitCompiled waits for the runtime's first message: compiled, or a fatal
// parse error. The runtime's output channel is handed to the bridge afterwards.
func awaitCompiled(ctx context.Context, out io.Writer, rt *remote4.Runtime) (remote4.Message, error) {
	timer := time.NewTimer(runtimeReadyTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return remote4.Message{}, ctx.Err()
		case <-timer.C:
			return remote4.Message{}, clierr.WithCode(clierr.User("the local runtime didn't start within 30s",
				"re-run with --lua pointing at a Lua 5.1 interpreter"), codeRuntimeUnavailable)
		case m, ok := <-rt.Messages():
			if !ok {
				return remote4.Message{}, clierr.Wrap(errOrExit(rt.Err()), "the local runtime exited before loading the driver", "")
			}
			switch m.Type {
			case "compiled":
				return m, nil
			case "fatal":
				return remote4.Message{}, clierr.WithCode(clierr.UserT("Driver failed to compile", m.Error,
					"fix the error and run `driverforge run` again"), codeBuildFailed)
			case "print", "raw":
				ui.Fprintln(out, "  | "+m.Text)
			}
		}
	}
}

func loadSDKBundle(ctx context.Context, out io.Writer, cfg *driverforgeconfig.Config) []byte {
	path, _, err := sdkrelease.EnsureCached(ctx, cfg.SDKVersion)
	if err != nil {
		ui.Fprintln(out, ui.Warning("SDK unavailable ("+err.Error()+"); the stub runs uninstrumented"))
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// refreshStubToken re-mints the broker token ahead of expiry and pushes it to
// the stub, which uses it for its own reconnects.
func refreshStubToken(ctx context.Context, bridge *remote4.Bridge, current string) {
	t := time.NewTicker(tokenRefreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tok, err := auth.MQTTAccessToken(ctx)
			if err != nil || tok == current {
				continue
			}
			if bridge.SendToken(tok) == nil {
				current = tok
			}
		}
	}
}

func renderRunEvents(ctx context.Context, out io.Writer, render func(remote4.Event), bridge *remote4.Bridge, bridgeDone <-chan error, stubHandlers []string) error {
	loadSent := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-bridgeDone:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return clierr.Wrap(err, "the session stopped", "")
		case e, ok := <-bridge.Events():
			if !ok {
				continue
			}
			render(e)
			if err := onRunEvent(out, bridge, e, stubHandlers, &loadSent); err != nil {
				return err
			}
		}
	}
}

// onRunEvent reacts to the events that drive the session, beyond rendering
// them.
func onRunEvent(out io.Writer, bridge *remote4.Bridge, e remote4.Event, stubHandlers []string, loadSent *bool) error {
	switch e.Kind {
	case remote4.EventStubOnline:
		// The driver runs only now: its file-scope C4 calls need the stub.
		if *loadSent {
			return nil
		}
		*loadSent = true
		if err := bridge.Load(e.Message.Ctx); err != nil {
			return clierr.Wrap(err, "couldn't start the driver locally", "")
		}
	case remote4.EventReady:
		if missing := remote4.Missing(stubHandlers, e.Message.Handlers); len(missing) > 0 {
			ui.Fprintln(out, ui.Warning("defined at runtime, not forwarded by the stub: "+strings.Join(missing, ", ")+
				" — define them at file scope, then restart `driverforge run`"))
		}
	}
	return nil
}

func renderRunEvent(out io.Writer, e remote4.Event) {
	m := e.Message
	switch e.Kind {
	case remote4.EventStubOnline:
		ui.Fprintln(out, ui.Success("Stub online as "+firstNonEmpty(m.Driver, "driver")+"; starting the driver locally"))
	case remote4.EventReady:
		ui.Fprintln(out, ui.Success(fmt.Sprintf("Driver running locally (%d handlers)", len(m.Handlers))))
	case remote4.EventStubOffline:
		ui.Fprintln(out, ui.Warning("stub went offline"))
	case remote4.EventStubLog:
		if e.Err != nil {
			ui.Fprintln(out, ui.Warning(e.Err.Error()))
		} else {
			ui.Fprintln(out, ui.Muted("  stub "+m.Level+": "+m.Text))
		}
	case remote4.EventCallback:
		ui.Fprintln(out, "← "+m.Summary())
	case remote4.EventCallbackResult:
		if m.OK != nil && !*m.OK {
			ui.Fprintln(out, ui.Failure("  "+indentError(m.Error)))
		} else if flagVerbose && m.Handled != nil && !*m.Handled {
			ui.Fprintln(out, ui.Muted("  (no local handler for "+m.Name+")"))
		}
	case remote4.EventC4Call:
		if flagVerbose {
			ui.Fprintln(out, ui.Muted("  → "+m.Summary()))
		}
	case remote4.EventC4Result:
		if m.OK != nil && !*m.OK {
			ui.Fprintln(out, ui.Failure("  ✗ C4 call failed: "+m.Error))
		} else if flagVerbose {
			ui.Fprintln(out, ui.Muted("  ← "+trimRaw(m.Results)))
		}
	case remote4.EventPrint:
		ui.Fprintln(out, "  | "+m.Text)
	case remote4.EventReloaded:
		if m.OK != nil && *m.OK {
			ui.Fprintln(out, ui.Success("Reloaded driver source"))
		} else {
			ui.Fprintln(out, ui.Failure("✗ Reload failed, previous code still running:\n  "+indentError(m.Error)))
		}
	case remote4.EventFatal:
		ui.Fprintln(out, ui.Failure("✗ "+m.Error))
	case remote4.EventRuntimeExited:
		ui.Fprintln(out, ui.Failure("✗ local runtime exited: "+errString(e.Err)))
	case remote4.EventRuntimeLog:
		if e.Err != nil {
			ui.Fprintln(out, ui.Warning(e.Err.Error()))
		} else if m.Text != "" {
			ui.Fprintln(out, ui.Muted("  runtime "+m.Level+": "+m.Text))
		}
	}
}

func endWithContext(ctx context.Context, bridge *remote4.Bridge) error {
	done := make(chan error, 1)
	go func() { done <- bridge.End() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func indentError(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n  ")
}

func trimRaw(b []byte) string {
	s := string(b)
	if len(s) > 160 {
		return s[:157] + "…"
	}
	return s
}

func errString(err error) string {
	if err == nil {
		return "unknown"
	}
	return err.Error()
}

func errOrExit(err error) error {
	if err == nil {
		return errors.New("exited")
	}
	return err
}

// runEventKinds names each event kind in structured output.
var runEventKinds = map[remote4.EventKind]string{
	remote4.EventCompiled:       "compiled",
	remote4.EventReady:          "ready",
	remote4.EventCallback:       "callback",
	remote4.EventCallbackResult: "callback_result",
	remote4.EventC4Call:         "c4_call",
	remote4.EventC4Result:       "c4_result",
	remote4.EventPrint:          "print",
	remote4.EventRuntimeLog:     "runtime_log",
	remote4.EventReloaded:       "reloaded",
	remote4.EventFatal:          "fatal",
	remote4.EventStubOnline:     "stub_online",
	remote4.EventStubOffline:    "stub_offline",
	remote4.EventStubLog:        "stub_log",
	remote4.EventRuntimeExited:  "runtime_exited",
}

// runEventOut is one run session event in structured output. Unlike the
// text view it reports every event, verbose or not; fields an event doesn't
// carry are left out.
type runEventOut struct {
	Event    string          `json:"event"`
	Summary  string          `json:"summary,omitempty"`
	Name     string          `json:"name,omitempty"`
	Method   string          `json:"method,omitempty"`
	Args     json.RawMessage `json:"args,omitempty"`
	Results  json.RawMessage `json:"results,omitempty"`
	OK       *bool           `json:"ok,omitempty"`
	Handled  *bool           `json:"handled,omitempty"`
	Level    string          `json:"level,omitempty"`
	Text     string          `json:"text,omitempty"`
	Error    string          `json:"error,omitempty"`
	Handlers []string        `json:"handlers,omitempty"`
	Driver   string          `json:"driver,omitempty"`
}

func runEventOf(e remote4.Event) runEventOut {
	m := e.Message
	o := runEventOut{
		Event: firstNonEmpty(runEventKinds[e.Kind], "unknown"),
		Name:  m.Name, Method: m.Method, Args: m.Args, Results: m.Results,
		OK: m.OK, Handled: m.Handled, Level: m.Level, Text: m.Text, Error: m.Error,
		Handlers: m.Handlers, Driver: m.Driver,
	}
	switch e.Kind {
	case remote4.EventCallback, remote4.EventC4Call:
		o.Summary = m.Summary()
	}
	if o.Error == "" && e.Err != nil {
		o.Error = e.Err.Error()
	}
	return o
}
