package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// newDebugCmd starts a Remote4 live-debug session: the driver's Lua runs in
// a local process while a stub on the controller forwards every callback to
// it and executes its C4 calls. See docs/remote4-protocol.md in the platform repo.
func newDebugCmd() *cobra.Command {
	var o debugOptions
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Run the driver locally against a real controller (live debug)",
		Long: `Run your driver's Lua on this machine while a real controller drives it.

A stub driver replaces yours on the controller for the session. It forwards
every callback (ReceivedFromProxy, OnPropertyChanged, OnTimerExpired, …) to a
local Lua process running your source, and executes the C4 API calls your
code makes against the real controller. Save a file and the next callback runs
the new code. Attach your usual Lua debugger to the local process.

Ctrl+C ends the session; run 'driverforge deploy' to restore your driver.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDebug(cmd, o)
		},
	}
	cmd.Flags().StringVarP(&o.configuration, "configuration", "c", "", "build configuration to debug (selects config.<name>.lua)")
	cmd.Flags().StringVar(&o.device, "device", "", "controller to debug on (name, hostname, or id)")
	cmd.Flags().BoolVar(&o.selectDevice, "select", false, "re-select the target controller first")
	cmd.Flags().StringVar(&o.lua, "lua", "", "Lua 5.1 interpreter to run the driver with (default: lua5.1, luajit or lua on PATH)")
	cmd.Flags().StringVar(&o.mobdebug, "mobdebug", "", "start mobdebug in the local process and connect to host:port (ZeroBrane, VS Code)")
	cmd.Flags().BoolVar(&o.noReload, "no-reload", false, "don't reload the driver when source files change")
	return cmd
}

type debugOptions struct {
	configuration string
	device        string
	selectDevice  bool
	lua           string
	mobdebug      string
	noReload      bool
}

const (
	runtimeReadyTimeout = 30 * time.Second
	tokenRefreshEvery   = 5 * time.Minute
	runtimeStopTimeout  = 3 * time.Second
)

// debugEnv is everything a session needs before it touches the controller.
type debugEnv struct {
	proj     project.Project
	cfg      *driverforgeconfig.Config
	userID   string
	token    string
	endpoint remote4.BrokerEndpoint
	dev      orgctx.Device
	session  string
}

func prepareDebug(ctx context.Context, out io.Writer, o debugOptions) (debugEnv, error) {
	var env debugEnv
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
		return env, clierr.User("not signed in", "run `driverforge login` first")
	}
	token, err := auth.MQTTAccessToken(ctx)
	if err != nil {
		if errors.Is(err, auth.ErrSessionExpired) {
			return env, clierr.WrapT(err, "Session expired", "your sign-in has expired, so the debug session can't start",
				"run `driverforge login`, then `driverforge debug` again")
		}
		if ctx.Err() != nil {
			return env, err
		}
		return env, clierr.WrapT(err, "Driverforge unreachable", "couldn't reach Driverforge to start the debug session",
			"check your connection and try again")
	}
	endpoint, err := remote4.ResolveBroker()
	if err != nil {
		return env, err
	}
	dev, err := resolveDeployDevice(ctx, out, o.device, o.selectDevice)
	if err != nil {
		return env, err
	}
	return debugEnv{proj: proj, cfg: cfg, userID: creds.Subject, token: token, endpoint: endpoint, dev: dev, session: newSessionID()}, nil
}

// buildStub builds the real driver as deploy would, unencrypted, then swaps its
// script for the stub bundle. driver.xml is untouched. It returns the stub
// package's path and the driver's artifact name.
func buildStub(ctx context.Context, cmd *cobra.Command, out io.Writer, o debugOptions, env debugEnv, handlers []string) (stubPath, artifact string, err error) {
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

func connectDebugBroker(ctx context.Context, out io.Writer, env debugEnv) (remote4.Broker, error) {
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
		return nil, clierr.Wrap(err, "couldn't connect to the Anvil broker at "+env.endpoint.URL,
			"check your network, or set DRIVERFORGE_MQTT_URL for a local stack")
	}
	return broker, nil
}

func runDebug(cmd *cobra.Command, o debugOptions) error {
	out := cmd.OutOrStdout()
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env, err := prepareDebug(ctx, out, o)
	if err != nil {
		return err
	}
	ui.Fprintln(out, "\n"+ui.DeployHeading("Debugging "+env.cfg.Project.Name+" on", env.dev.Name))
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
		MobDebug: o.mobdebug, Stderr: out,
	})
	if err != nil {
		return clierr.Wrap(err, "couldn't start the local Lua runtime",
			"install Lua 5.1 (brew install lua@5.1) or point --lua at an interpreter")
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
	broker, err := connectDebugBroker(ctx, out, env)
	if err != nil {
		return err
	}
	defer broker.Close()
	ui.Fprintln(out, ui.Success("Connected to broker "+env.endpoint.Host))

	bridge := remote4.NewBridge(rt, broker, env.userID, env.session)
	bridgeDone := make(chan error, 1)
	go func() { bridgeDone <- bridge.Run(ctx) }()

	// 4. Swap the stub in on the controller.
	if err := runDeployToDevice(ctx, out, env.dev, stubPath, artifact, false); err != nil {
		return err
	}

	// 5. Reload on save, keep the stub's token fresh, render the session.
	if !o.noReload {
		go remote4.Watch(ctx, env.proj.SrcDir, 0, func(changed []string) {
			ui.Fprintln(out, ui.Muted("  changed: "+strings.Join(changed, ", ")))
			_ = bridge.Reload()
		})
	}
	go refreshStubToken(ctx, bridge, env.token)

	ui.Fprintln(out, "\n"+ui.Success("Debug session active. Ctrl+C to stop.")+"\n")
	err = renderDebugEvents(ctx, out, bridge, bridgeDone, handlers)

	// Teardown in order: tell the stub, stop the runtime, then disconnect.
	endCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = endWithContext(endCtx, bridge)
	ui.Fprintln(out, "\n"+ui.Muted("Debug session ended.")+" Run `driverforge deploy` to restore your driver.")
	return err
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
			return remote4.Message{}, clierr.User("the local runtime didn't start within 30s",
				"re-run with --lua pointing at a Lua 5.1 interpreter")
		case m, ok := <-rt.Messages():
			if !ok {
				return remote4.Message{}, clierr.Wrap(errOrExit(rt.Err()), "the local runtime exited before loading the driver", "")
			}
			switch m.Type {
			case "compiled":
				return m, nil
			case "fatal":
				return remote4.Message{}, clierr.UserT("Driver failed to compile", m.Error,
					"fix the error and run `driverforge debug` again")
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

func renderDebugEvents(ctx context.Context, out io.Writer, bridge *remote4.Bridge, bridgeDone <-chan error, stubHandlers []string) error {
	loadSent := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-bridgeDone:
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return clierr.Wrap(err, "the debug session stopped", "")
		case e, ok := <-bridge.Events():
			if !ok {
				continue
			}
			renderDebugEvent(out, e)
			if err := onDebugEvent(out, bridge, e, stubHandlers, &loadSent); err != nil {
				return err
			}
		}
	}
}

// onDebugEvent reacts to the events that drive the session, beyond rendering
// them.
func onDebugEvent(out io.Writer, bridge *remote4.Bridge, e remote4.Event, stubHandlers []string, loadSent *bool) error {
	switch e.Kind {
	case remote4.EventStubOnline:
		// The driver runs only now: its file-scope C4 calls need the stub.
		if *loadSent {
			return nil
		}
		*loadSent = true
		if err := bridge.Load(); err != nil {
			return clierr.Wrap(err, "couldn't start the driver locally", "")
		}
	case remote4.EventReady:
		if missing := remote4.Missing(stubHandlers, e.Message.Handlers); len(missing) > 0 {
			ui.Fprintln(out, ui.Warning("defined at runtime, not forwarded by the stub: "+strings.Join(missing, ", ")+
				" — define them at file scope, then restart `driverforge debug`"))
		}
	}
	return nil
}

func renderDebugEvent(out io.Writer, e remote4.Event) {
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
