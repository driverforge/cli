package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

func newDeviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "device",
		Short: "Manage the active deploy target (controller)",
	}
	cmd.AddCommand(newDeviceListCmd(), newDeviceSelectCmd(), newDeviceShowCmd(), newDeviceCurrentCmd(), newDeviceScanCmd(), newDeviceRefreshNavigatorsCmd(), newDeviceCapabilitiesCmd())
	return cmd
}

// knownCapabilities is every agent capability this CLI build understands, in
// protocol order, with what each one unlocks — the diagnosis vocabulary for
// `device capabilities`.
var knownCapabilities = []struct{ name, what string }{
	{"deploy", "full driver deploy (stage + reinstall)"},
	{"sync", "Lua hot-swap without a reload"},
	{"driver-state", "installed-driver probe (sync baseline)"},
	{agent.CapabilityDeployStream, "streaming deploy progress"},
	{agent.CapabilityBootstrap, "first-install wizard"},
	{agent.CapabilityChecksum, "upload checksum verification"},
	{agent.CapabilityNavigatorRefresh, "navigator refresh after UI changes"},
}

// newDeviceCapabilitiesCmd reports what the connected agent advertises —
// the diagnostic for "why is the CLI skipping X on this controller": every
// capability this CLI knows (advertised or missing), plus anything the agent
// advertises that this CLI build doesn't know yet.
func newDeviceCapabilitiesCmd() *cobra.Command {
	var (
		deviceFlag string
		selectFlag bool
	)
	cmd := &cobra.Command{
		Use:   "capabilities",
		Short: "Show what the agent on a controller advertises (protocol, capabilities)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			dev, err := resolveSyncDevice(ctx, out, deviceFlag, selectFlag)
			if err != nil {
				return err
			}
			var h agent.Health
			werr := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI),
				fmt.Sprintf("Connecting to %s…", dev.Name),
				func() (string, error) {
					var herr error
					_, h, herr = dialAgent(ctx, dev)
					if herr != nil {
						return "", herr
					}
					return connectedLabel(dev, h), nil
				})
			if werr != nil {
				return agentUnreachableErr(dev, werr)
			}

			fmt.Fprintf(out, "\n%s  ·  Anvil Agent %s  ·  protocol %d\n\n",
				controllerLabel(dev, h), firstNonEmpty(h.AgentVersion, "unknown"), h.Protocol)

			width := 0
			for _, k := range knownCapabilities {
				if len(k.name) > width {
					width = len(k.name)
				}
			}
			known := map[string]bool{}
			for _, k := range knownCapabilities {
				known[k.name] = true
				if h.Supports(k.name) {
					fmt.Fprintf(out, "  %s %-*s  %s\n", ui.StatusOK("✓"), width, k.name, ui.StatusMuted(k.what))
				} else {
					fmt.Fprintf(out, "  %s %s\n", ui.StatusMuted("–"), ui.StatusMuted(fmt.Sprintf("%-*s  %s (not advertised — upgrade the agent)", width, k.name, k.what)))
				}
			}
			// Capabilities from a future agent this CLI build predates.
			for _, c := range h.Capabilities {
				if !known[c] {
					fmt.Fprintf(out, "  %s %-*s  %s\n", ui.StatusWarn("?"), width, c, ui.StatusMuted("unknown to this CLI — upgrade driverforge"))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&deviceFlag, "device", "", "controller to inspect (name, hostname, or id)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "re-select the target controller first")
	return cmd
}

// newDeviceRefreshNavigatorsCmd triggers a Control4 navigator refresh on
// demand — navigators cache each driver's UI schema, so a UI that looks stale
// (old screens, endless spinners) needs this kick to re-read it. Deploys that
// change driver.xml trigger it automatically; this is the standalone lever.
func newDeviceRefreshNavigatorsCmd() *cobra.Command {
	var (
		deviceFlag string
		selectFlag bool
	)
	cmd := &cobra.Command{
		Use:   "refresh-navigators",
		Short: "Make navigators re-read cached driver UI (screens, tabs)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			dev, err := resolveSyncDevice(ctx, out, deviceFlag, selectFlag)
			if err != nil {
				return err
			}
			cl, h, herr := dialAgent(ctx, dev)
			if herr != nil {
				return agentUnreachableErr(dev, herr)
			}
			if !h.Supports(agent.CapabilityNavigatorRefresh) {
				return agentMissingCapabilityErr(dev, "navigator refresh", h.AgentVersion)
			}
			rctx, cancel := context.WithTimeout(ctx, navigatorRefreshTimeout)
			defer cancel()
			return ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI),
				fmt.Sprintf("Refreshing navigators on %s…", controllerLabel(dev, h)),
				func() (string, error) {
					if err := cl.RefreshNavigators(rctx); err != nil {
						return "", err
					}
					return "Navigators refreshed on " + controllerLabel(dev, h), nil
				})
		},
	}
	cmd.Flags().StringVar(&deviceFlag, "device", "", "controller to refresh (name, hostname, or id)")
	cmd.Flags().BoolVar(&selectFlag, "select", false, "re-select the target controller first")
	return cmd
}

// newDeviceCurrentCmd prints just the active device's name — a porcelain command
// for scripts and shell prompts (e.g. a Spaceship section). It reads the local
// context only (no network), and when no device is selected for the current org
// it prints nothing and exits non-zero silently, so a prompt shows nothing
// rather than an error.
func newDeviceCurrentCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "current",
		Short:  "Print the active device's name (for scripts and prompts)",
		Args:   cobra.NoArgs,
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := orgctx.CurrentDevice()
			if err != nil {
				return clierr.Silent()
			}
			fmt.Fprintln(cmd.OutOrStdout(), d.Name)
			return nil
		},
	}
}

// newDeviceListCmd lists the devices registered to the current organization.
func newDeviceListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List devices registered to the current organization",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			org, err := orgctx.Current()
			if err != nil {
				return err
			}
			devices, err := driverforgeapi.New().ListDevices(cmd.Context(), org.ID)
			if err != nil {
				return err
			}
			if len(devices) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No devices registered to %s yet.\n", org.Name)
				return nil
			}
			current, _ := orgctx.CurrentDevice()
			if ui.Interactive(flagNoTUI) {
				// Live view: graph rows with a presence dot that turns green as
				// each controller is seen on the network (SSDP).
				return runDeviceListTUI(cmd.Context(), devices, current.ID)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "\tNAME\tHOST\tSTATUS")
			for _, d := range devices {
				mark := " "
				if d.ID == current.ID {
					mark = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", mark, d.DisplayName(), hostOf(d), d.Status)
			}
			return tw.Flush()
		},
	}
}

// newDeviceSelectCmd selects the active device. With an argument it matches by
// deviceId, UUID, hostname, or friendly name; with no argument it shows a picker.
func newDeviceSelectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "select [name|hostname|id]",
		Short: "Select the active deploy target",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			org, err := orgctx.Current()
			if err != nil {
				return err
			}
			devices, err := driverforgeapi.New().ListDevices(cmd.Context(), org.ID)
			if err != nil {
				return err
			}
			active := filterActive(devices)
			if len(active) == 0 {
				return noActiveDevicesErr(org.Name)
			}

			if len(args) == 1 {
				d := findDevice(active, args[0])
				if d == nil {
					return clierr.User(fmt.Sprintf("device %q not found", args[0]), "run `driverforge device list`")
				}
				return applyDevice(cmd.Context(), cmd.OutOrStdout(), org.ID, *d)
			}
			if !ui.Interactive(flagNoTUI) {
				return fmt.Errorf("specify a device: `driverforge device select <name|id>`")
			}
			remembered, _ := orgctx.CurrentDevice()
			d, err := pickDevice(cmd.Context(), active, remembered.ID)
			if err != nil {
				return err
			}
			return applyDevice(cmd.Context(), cmd.OutOrStdout(), org.ID, d)
		},
	}
}

// newDeviceShowCmd prints the active device.
func newDeviceShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show the active deploy target",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := orgctx.CurrentDevice()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), d.Name)
			return nil
		},
	}
}

// --- shared helpers (used by device commands, deploy, and build --deploy) ---

// noActiveDevicesErr is the expected condition where an org has no ACTIVE
// controllers to target. It's a user-facing warning (friendly amber card, never
// reported), not a bug — register a controller and it resolves.
func noActiveDevicesErr(orgName string) error {
	return clierr.UserT("No devices",
		fmt.Sprintf("No active devices in %s yet.", orgName),
		"register a controller (install the Anvil Agent) first",
	)
}

// resolveDeployDevice determines the device to deploy to: an explicit pref
// (--device), else the remembered device for the current org (unless
// forceSelect), else an interactive pick. It requires a logged-in user and a
// selected org, and never auto-picks a default — deploying to the wrong
// controller is risky, so the choice is always explicit.
func resolveDeployDevice(ctx context.Context, out io.Writer, pref string, forceSelect bool) (orgctx.Device, error) {
	org, err := orgctx.Current()
	if err != nil {
		return orgctx.Device{}, err
	}
	// Listing the org's controllers is a graph round-trip (a second or two) that
	// runs on every deploy — show a transient spinner so it doesn't look stalled.
	var devices []driverforgeapi.Device
	if werr := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), "Finding your controllers…", func() (string, error) {
		var e error
		devices, e = driverforgeapi.New().ListDevices(ctx, org.ID)
		return "Controllers discovered", e
	}); werr != nil {
		return orgctx.Device{}, werr
	}
	active := filterActive(devices)
	if len(active) == 0 {
		return orgctx.Device{}, noActiveDevicesErr(org.Name)
	}

	if pref != "" {
		d := findDevice(active, pref)
		if d == nil {
			return orgctx.Device{}, clierr.User(fmt.Sprintf("device %q not found", pref), "run `driverforge device list`")
		}
		return persistDevice(ctx, out, org.ID, *d)
	}

	if !forceSelect {
		if cur, err := orgctx.CurrentDevice(); err == nil && findByDeviceID(active, cur.ID) != nil {
			return cur, nil // remembered and still valid
		}
	}

	if !ui.Interactive(flagNoTUI) {
		return orgctx.Device{}, clierr.User("no device selected", "pass --device <name|id>, or run `driverforge device select`")
	}
	remembered, _ := orgctx.CurrentDevice()
	d, err := pickDevice(ctx, active, remembered.ID)
	if err != nil {
		return orgctx.Device{}, err
	}
	return persistDevice(ctx, out, org.ID, d)
}

// deviceResolver resolves the target device inside a run frame — a framed
// ship's device step ("Finding your controllers…") belongs in the frame, not
// as a separate spinner above it.
type deviceResolver func(ctx context.Context, f *ui.Frame) (orgctx.Device, error)

// resolvedDevice wraps an already-resolved device as an instant resolver — for
// sync's escalation to a full deploy (reusing the device sync resolved) and
// the picker path (the pick necessarily ran before the frame).
func resolvedDevice(dev orgctx.Device) deviceResolver {
	return func(context.Context, *ui.Frame) (orgctx.Device, error) { return dev, nil }
}

// frameDeviceResolution decides how a framed ship resolves its target device.
// A choice that needs the interactive picker (forced re-select, or nothing
// remembered and no --device) runs before the frame — the picker is its own
// program and can't run inside one. Everything else resolves inside the frame
// as a "Finding your controllers…" step: a --device pref is looked up, a
// remembered device revalidated against the org's active list. target is the
// display name for the frame heading (best-effort before resolution).
func frameDeviceResolution(ctx context.Context, out io.Writer, pref string, forceSelect bool) (target string, resolve deviceResolver, err error) {
	// --device wins over --select, mirroring resolveDeployDevice.
	if pref != "" {
		return pref, func(ctx context.Context, f *ui.Frame) (orgctx.Device, error) {
			return lookupDeviceInFrame(ctx, f, pref)
		}, nil
	}
	if !forceSelect {
		if cur, cerr := orgctx.CurrentDevice(); cerr == nil {
			return cur.Name, func(ctx context.Context, f *ui.Frame) (orgctx.Device, error) {
				return validateRememberedDeviceInFrame(ctx, f, cur)
			}, nil
		}
	}
	dev, err := resolveDeployDevice(ctx, out, "", forceSelect)
	if err != nil {
		return "", nil, err
	}
	return dev.Name, resolvedDevice(dev), nil
}

// findControllersInFrame lists the org's active controllers as a frame step —
// the framed counterpart of resolveDeployDevice's discovery spinner.
func findControllersInFrame(ctx context.Context, f *ui.Frame) (orgID string, active []driverforgeapi.Device, err error) {
	f.Step("Finding your controllers…")
	org, err := orgctx.Current()
	if err != nil {
		return "", nil, err
	}
	devices, err := driverforgeapi.New().ListDevices(ctx, org.ID)
	if err != nil {
		return "", nil, err
	}
	f.Logf("✓ Controllers discovered")
	active = filterActive(devices)
	if len(active) == 0 {
		return "", nil, noActiveDevicesErr(org.Name)
	}
	return org.ID, active, nil
}

// lookupDeviceInFrame resolves a --device pref against the org's controllers
// inside the frame and persists the choice (quietly — the confirmation is a
// log line, not a print above the frame).
func lookupDeviceInFrame(ctx context.Context, f *ui.Frame, pref string) (orgctx.Device, error) {
	orgID, active, err := findControllersInFrame(ctx, f)
	if err != nil {
		return orgctx.Device{}, err
	}
	d := findDevice(active, pref)
	if d == nil {
		return orgctx.Device{}, clierr.User(fmt.Sprintf("device %q not found", pref), "run `driverforge device list`")
	}
	dev, err := persistDevice(ctx, io.Discard, orgID, *d)
	if err != nil {
		return orgctx.Device{}, err
	}
	f.Logf("✓ Using %s", dev.Name)
	return dev, nil
}

// validateRememberedDeviceInFrame confirms the remembered device is still an
// active controller in the org before deploying to it. A device that vanished
// (deleted/re-registered) is a friendly condition pointing at re-selection —
// the picker itself can't run inside the frame.
func validateRememberedDeviceInFrame(ctx context.Context, f *ui.Frame, cur orgctx.Device) (orgctx.Device, error) {
	_, active, err := findControllersInFrame(ctx, f)
	if err != nil {
		return orgctx.Device{}, err
	}
	if findByDeviceID(active, cur.ID) == nil {
		return orgctx.Device{}, clierr.User(
			fmt.Sprintf("%s is no longer registered in this org", cur.Name),
			"run `driverforge device select` to pick a controller")
	}
	return cur, nil
}

// applyDevice persists the chosen device and prints confirmation.
func applyDevice(ctx context.Context, out io.Writer, orgID string, d driverforgeapi.Device) error {
	_, err := persistDevice(ctx, out, orgID, d)
	return err
}

func persistDevice(ctx context.Context, out io.Writer, orgID string, d driverforgeapi.Device) (orgctx.Device, error) {
	od := orgctx.Device{ID: d.ID, Name: d.DisplayName(), OrgID: orgID, DeviceID: d.DeviceID, Hostname: d.Hostname, IP: d.IP}
	// Cache the controller's pinned TLS fingerprint now (best-effort); the
	// connect path refetches/refuses if it's still unavailable.
	if fp, err := driverforgeapi.New().DeviceFingerprint(ctx, d.DeviceID); err == nil {
		od.TLSFingerprint = fp
	}
	if err := orgctx.SetCurrentDevice(od); err != nil {
		return orgctx.Device{}, fmt.Errorf("saving device: %w", err)
	}
	fmt.Fprintf(out, "Using device: %s\n", od.Name)
	return od, nil
}

// filterActive keeps only ACTIVE devices (the only valid deploy targets).
func filterActive(devices []driverforgeapi.Device) []driverforgeapi.Device {
	out := make([]driverforgeapi.Device, 0, len(devices))
	for _, d := range devices {
		if strings.EqualFold(d.Status, "ACTIVE") {
			out = append(out, d)
		}
	}
	return out
}

// findDevice matches an arg against deviceId, UUID id, hostname, or friendly
// name (case-insensitive except the UUID).
func findDevice(devices []driverforgeapi.Device, arg string) *driverforgeapi.Device {
	lower := strings.ToLower(arg)
	for i := range devices {
		d := &devices[i]
		if d.ID == arg ||
			strings.EqualFold(d.DeviceID, arg) ||
			strings.ToLower(d.Hostname) == lower ||
			strings.EqualFold(d.FriendlyName, arg) {
			return d
		}
	}
	return nil
}

func findByDeviceID(devices []driverforgeapi.Device, id string) *driverforgeapi.Device {
	for i := range devices {
		if devices[i].ID == id {
			return &devices[i]
		}
	}
	return nil
}

// hostOf is the controller's connectable address. Prefer the IP: a Control4
// hostname is a controller identifier ("core1-<MAC>"), not a resolvable DNS
// name, so dialing it fails — especially across a VLAN.
func hostOf(d driverforgeapi.Device) string {
	if d.IP != "" {
		return d.IP
	}
	return d.Hostname
}
