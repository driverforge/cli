package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/deploycache"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

// newSyncCmd hot-swaps a driver's Lua into the running driver on a controller —
// fast, no reload. It only does so when driver.xml hasn't changed structurally
// since the running build; otherwise it offers to escalate to a full deploy.
func newSyncCmd() *cobra.Command {
	var (
		configuration string
		deviceFlag    string
		selectFlag    bool
		deployFlag    bool
		refreshFlag   bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Build and hot-swap a driver's Lua on a device without a full reload",
		Long: "Build the driver, then hot-swap the Lua into the running driver — fast,\n" +
			"no reload. What you sync is always your current source.\n\n" +
			"If driver.xml changed structurally (actions, properties, etc.), a full\n" +
			"deploy is required and sync will offer to do one. Note: sync re-runs\n" +
			"top-level Lua but not OnDriverInit/LateInit, so keep load-time init\n" +
			"idempotent; and a Composer edit isn't detected (deploy is the fix).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ui.Interactive(flagNoTUI) {
				return runSyncFramed(cmd, configuration, deviceFlag, selectFlag, deployFlag, refreshFlag)
			}
			// A full deploy (asked for, or escalated into) rebuilds with the
			// next version — Director won't reload an unchanged <version>.
			deployFn := func() error {
				res, err := buildForDeploy(cmd, configuration)
				if err != nil {
					return err
				}
				return runDeploy(cmd.Context(), cmd.OutOrStdout(), res.C4zPath, res.ArtifactName, deviceFlag, selectFlag, refreshFlag)
			}
			if deployFlag {
				return deployFn()
			}
			res, err := buildForShip(cmd, configuration)
			if err != nil {
				return err
			}
			return runSync(cmd.Context(), cmd.OutOrStdout(), res.C4zPath, res.ArtifactName, deviceFlag, selectFlag, deployFn)
		},
	}
	cmd.Flags().StringVarP(&configuration, "configuration", "c", "", "build configuration to build and sync")
	cmd.Flags().StringVar(&deviceFlag, "device", "", "device to sync to (name, hostname, or id)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "re-select the target device")
	cmd.Flags().BoolVar(&deployFlag, "deploy", false, "do a full deploy instead of a hot-swap")
	cmd.Flags().BoolVar(&refreshFlag, "refresh-navigators", false, "when this sync escalates to a deploy, refresh navigators even if driver.xml didn't change")
	return cmd
}

// runSync performs the fast-path decision (see plan Part 3) for the artifact
// `driverforge sync` just built. deployFn is the full-deploy fallback — it rebuilds
// with the next version and deploys (see newSyncCmd).
func runSync(ctx context.Context, out io.Writer, c4zPath, artifact, pref string, forceSelect bool, deployFn func() error) error {
	filename := artifact + ".c4z"

	// Hot-swap ships the plaintext squished Lua; encrypted builds can't (the
	// director can't run ciphertext as LUA_COMMANDS) → full deploy.
	lua, err := readC4zEntry(c4zPath, "driver.lua")
	if err != nil {
		if _, encErr := readC4zEntry(c4zPath, "driver.lua.encrypted"); encErr == nil {
			ui.Fprintln(out, "\n"+ui.Warning("encrypted build — hot-swap isn't supported; doing a full deploy."))
			return deployFn()
		}
		return err
	}
	hash, err := structuralHashOfC4z(c4zPath)
	if err != nil {
		return err
	}

	// The heading titles the whole operation and sits above the resolve/connect
	// steps that follow — mirroring runDeploy. Lead with the remembered
	// controller when we have one; otherwise print it once the picker decides.
	action := "Syncing " + artifact + " to"
	heading := ""
	if pref == "" && !forceSelect {
		if cur, cerr := orgctx.CurrentDevice(); cerr == nil {
			heading = cur.Name
		}
	}
	if heading != "" {
		ui.Fprintln(out, "\n"+ui.DeployHeading(action, heading))
	}

	dev, err := resolveSyncDevice(ctx, out, pref, forceSelect)
	if err != nil {
		return err
	}
	if heading == "" {
		ui.Fprintln(out, "\n"+ui.DeployHeading(action, dev.Name))
	}

	// Connecting resolves the pinned fingerprint and runs a health check — a
	// second or two of silent network work, so it gets a spinner that resolves
	// to ✓/✗. When the remembered-controller fast path skipped the discovery
	// step, this is the first step under the heading and takes the leading blank.
	connect := ui.WaitFor
	if heading == "" {
		connect = ui.WaitForStep // the discovery spinner already provided the gap
	}
	var (
		cl *agent.Client
		h  agent.Health
	)
	werr := connect(ctx, out, ui.Interactive(flagNoTUI),
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
		return agentUnreachableErr(dev, werr)
	}
	// Capability gate. An unreachable agent is a real error; an agent that's
	// reachable but can't hot-swap falls back to a full deploy.
	if !h.Supports("sync") {
		ui.Fprintln(out, "\n"+ui.Warning("agent doesn't support hot-swap — doing a full deploy."))
		return deployFn()
	}

	// Decide using the local baseline (fast); fall back to the agent's
	// driver-state probe on a cache miss.
	baseline := deploycache.Get(dev.ID, filename)
	if baseline == "" {
		if st, sErr := cl.DriverState(ctx, filename); sErr == nil && st.StructuralHash != "" {
			baseline = st.StructuralHash
			_ = deploycache.Set(dev.ID, filename, baseline)
		}
	}
	switch {
	case baseline == "":
		return escalate(out, "no verified baseline yet for this driver", deployFn)
	case baseline != hash:
		return escalate(out, "driver.xml changed (actions, properties, etc.)", deployFn)
	}

	// Structurally unchanged → hot-swap, behind a spinner that resolves.
	var res agent.SyncResult
	serr := ui.WaitForStep(ctx, out, ui.Interactive(flagNoTUI),
		"Hot-swapping the driver's Lua…",
		func() (string, error) {
			var e error
			res, e = cl.Sync(ctx, filename, hash, "", bytes.NewReader(lua))
			if e != nil {
				return "", e
			}
			return syncedLabel(res.Synced), nil
		})
	if serr != nil {
		// A structural mismatch the agent caught is a fallback, not a failure.
		if agent.Code(serr) == agent.CodeNeedsDeploy {
			return escalate(out, "driver.xml changed since the running build", deployFn)
		}
		return friendlySyncErr(dev, filename, serr)
	}
	_ = deploycache.Set(dev.ID, filename, firstNonEmpty(res.StructuralHash, hash))

	// An unverified swap still succeeded, so it warns above the success block —
	// the same shape the deploy path uses for confirmed-with-errors.
	if !res.Verified {
		ui.Fprintln(out, "\n"+ui.Warning("couldn't verify against a prior deploy — if you've edited driver.xml, run `driverforge deploy`"))
	}
	rows := [][2]string{{"controller", dev.Name}}
	if len(res.Devices) > 0 {
		rows = append(rows, [2]string{"devices", strings.Join(res.Devices, ", ")})
	}
	ui.Fprintln(out, "\n"+ui.SuccessBlock("sync complete", "", rows))
	return nil
}

// runSyncFramed is the interactive sync: the device resolves up front (the
// fast path reuses the remembered controller with zero UI), then the whole
// build + connect + hot-swap runs under a single in-place run frame. When a
// full deploy is needed instead, the frame ends amber ("Full deploy required")
// and the deploy runs as its own frame — after a confirm for a structural
// escalation, or straight away when a hot-swap was never possible (encrypted
// build, agent without the capability), mirroring the plain path.
func runSyncFramed(cmd *cobra.Command, configuration, pref string, forceSelect, deployFlag, forceRefresh bool) error {
	ctx := cmd.Context()
	out := cmd.OutOrStdout()
	defer metricsClient.Flush(ctx)

	// --deploy is a full deploy in sync clothing — resolve (and revalidate) the
	// device exactly as deploy does.
	if deployFlag {
		opts, err := deployBuildOptions(configuration)
		if err != nil {
			return err
		}
		target, resolve, err := frameDeviceResolution(ctx, out, pref, forceSelect)
		if err != nil {
			return err
		}
		return deployFramedTo(ctx, out, opts, configuration, target, resolve, forceRefresh)
	}

	// The fast path reuses the remembered controller directly — no validation
	// round-trip (mirroring resolveSyncDevice). Anything else resolves like
	// deploy: --device looks up inside the frame, the picker runs before it.
	var (
		target  string
		resolve deviceResolver
	)
	if pref == "" && !forceSelect {
		if cur, cerr := orgctx.CurrentDevice(); cerr == nil {
			target, resolve = cur.Name, resolvedDevice(cur)
		}
	}
	if resolve == nil {
		var rerr error
		target, resolve, rerr = frameDeviceResolution(ctx, out, pref, forceSelect)
		if rerr != nil {
			return rerr
		}
	}

	opts, err := shipBuildOptions(configuration)
	if err != nil {
		return err
	}

	var (
		dev            orgctx.Device
		fallbackReason string // hot-swap impossible → deploy without asking
		escalateReason string // structural change → confirm, then deploy
	)
	// fullDeployRequired resolves the frame amber: the sync didn't fail, it
	// just can't hot-swap — the follow-up deploy runs as its own frame.
	fullDeployRequired := func(f *ui.Frame, reason string) {
		f.Warnf("%s", reason)
		f.Step("Full deploy required")
	}
	err = ui.RunFrame(ctx, ui.FrameOptions{
		Step:        "Syncing to " + target + "…",
		DetailsOpen: flagVerbose,
	}, func(ctx context.Context, f *ui.Frame) error {
		res, err := buildInFrame(ctx, f, opts, configuration)
		if err != nil {
			return err
		}
		filename := res.ArtifactName + ".c4z"

		// Resolve the device before the hot-swap decisions, so every fallback
		// and escalation below already knows the deploy target.
		dev, err = resolve(ctx, f)
		if err != nil {
			return err
		}

		// Hot-swap ships the plaintext squished Lua; encrypted builds can't (the
		// director can't run ciphertext as LUA_COMMANDS) → full deploy.
		lua, err := readC4zEntry(res.C4zPath, "driver.lua")
		if err != nil {
			if _, encErr := readC4zEntry(res.C4zPath, "driver.lua.encrypted"); encErr == nil {
				fallbackReason = "encrypted build — hot-swap isn't supported"
				fullDeployRequired(f, fallbackReason)
				return nil
			}
			return err
		}
		hash, err := structuralHashOfC4z(res.C4zPath)
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
		// Capability gate. An unreachable agent is a real error; an agent that's
		// reachable but can't hot-swap falls back to a full deploy.
		if !h.Supports("sync") {
			fallbackReason = "agent doesn't support hot-swap"
			fullDeployRequired(f, fallbackReason)
			return nil
		}

		// Decide using the local baseline (fast); fall back to the agent's
		// driver-state probe on a cache miss.
		baseline := deploycache.Get(dev.ID, filename)
		if baseline == "" {
			if st, sErr := cl.DriverState(ctx, filename); sErr == nil && st.StructuralHash != "" {
				baseline = st.StructuralHash
				_ = deploycache.Set(dev.ID, filename, baseline)
			}
		}
		switch {
		case baseline == "":
			escalateReason = "no verified baseline yet for this driver"
		case baseline != hash:
			escalateReason = "driver.xml changed (actions, properties, etc.)"
		}
		if escalateReason != "" {
			fullDeployRequired(f, escalateReason)
			return nil
		}

		f.Step("Hot-swapping the driver's Lua…")
		sres, serr := cl.Sync(ctx, filename, hash, "", bytes.NewReader(lua))
		if serr != nil {
			// A structural mismatch the agent caught is a fallback, not a failure.
			if agent.Code(serr) == agent.CodeNeedsDeploy {
				escalateReason = "driver.xml changed since the running build"
				fullDeployRequired(f, escalateReason)
				return nil
			}
			return friendlySyncErr(dev, filename, serr)
		}
		_ = deploycache.Set(dev.ID, filename, firstNonEmpty(sres.StructuralHash, hash))
		f.Logf("✓ %s", syncedLabel(sres.Synced))

		// An unverified swap still succeeded, so it warns (amber frame) rather
		// than failing — the same shape the deploy path uses.
		if !sres.Verified {
			f.Warnf("couldn't verify against a prior deploy — if you've edited driver.xml, run `driverforge deploy`")
		}
		if len(sres.Devices) > 0 {
			f.Row("devices", strings.Join(sres.Devices, ", "))
		}
		f.Step("Sync complete")
		return nil
	})
	if err != nil {
		return err
	}
	// The framed full deploy reuses the device sync already resolved, so the
	// escalation never re-runs discovery or the picker.
	deployTo := func() error {
		dopts, derr := deployBuildOptions(configuration)
		if derr != nil {
			return derr
		}
		return deployFramedTo(ctx, out, dopts, configuration, dev.Name, resolvedDevice(dev), forceRefresh)
	}
	switch {
	case fallbackReason != "":
		return deployTo()
	case escalateReason != "":
		return escalate(out, escalateReason, deployTo)
	}
	return nil
}

// syncedLabel is the resolved ✓ line for a completed hot-swap.
func syncedLabel(n int) string {
	if n == 1 {
		return "Lua hot-swapped on 1 device"
	}
	return fmt.Sprintf("Lua hot-swapped on %d devices", n)
}

// friendlySyncErr translates the anticipated sync rejections into UserErrors —
// mirroring friendlyDeployErr: a structured agent answer means the CLI did its
// job, so it renders as a card and is never reported. Anything else (a genuine
// transport/decoding fault) stays reportable.
func friendlySyncErr(dev orgctx.Device, filename string, err error) error {
	// Already an anticipated, user-facing condition — e.g. a Ctrl-C during the
	// hot-swap spinner, which ui.WaitForStep maps to "Cancelled". Surface it as
	// it is rather than wrapping it into a reportable error.
	if clierr.IsUser(err) {
		return err
	}
	switch agent.Code(err) {
	case agent.CodeNotInstalled:
		return clierr.WrapT(err, "Driver not installed",
			fmt.Sprintf("%s isn't installed on %s", filename, dev.Name),
			"deploy it once in Composer Pro first")
	case agent.CodeSelfSyncForbidden:
		return clierr.WrapT(err, "Can't sync the agent", "the agent's own driver can't be hot-swapped", "use `driverforge deploy`")
	}
	var ae *agent.Error
	if errors.As(err, &ae) {
		return clierr.WrapT(err, "Sync failed", capitalise(ae.Error()), "")
	}
	return fmt.Errorf("sync: %w", err)
}

// resolveSyncDevice prefers the remembered device directly (no graph round-trip)
// for the fast path; it only lists devices when none is remembered or the user
// forces a choice.
func resolveSyncDevice(ctx context.Context, out io.Writer, pref string, forceSelect bool) (orgctx.Device, error) {
	if pref == "" && !forceSelect {
		if dev, err := orgctx.CurrentDevice(); err == nil {
			return dev, nil
		}
	}
	return resolveDeployDevice(ctx, out, pref, forceSelect)
}

// escalate explains why a full deploy is needed and, interactively, offers to
// do it now via deployFn (a fresh build with the next version, re-establishing
// the baseline).
func escalate(out io.Writer, reason string, deployFn func() error) error {
	if ui.Interactive(flagNoTUI) {
		ok, err := ui.Confirm("Run a full deploy now?", reason+" — a full deploy is required.", true)
		if err != nil {
			return err
		}
		if !ok {
			// User declined the escalation — exit non-zero but render nothing.
			return clierr.Silent()
		}
		return deployFn()
	}
	return clierr.User(reason, "run `driverforge deploy` (or `driverforge sync --deploy`)")
}
