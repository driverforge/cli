package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

// newDeviceNameCmd names the active controller. The name is the org-scoped
// friendlyName the Anvil UI shows, not a local nickname: naming a controller
// here renames it for everyone in the org, and `--device <name>` then targets
// it from deploy/sync (findDevice already matches on the friendly name).
//
// It names the ACTIVE device rather than taking a selector, so the flow is
// `device select` once, `device name` once, `--device <name>` forever after.
func newDeviceNameCmd() *cobra.Command {
	var clearFlag bool
	cmd := &cobra.Command{
		Use:   "name [name]",
		Short: "Name the active device (the name the Anvil UI shows)",
		Long: "Give the active controller a short name, so you can target it with\n" +
			"`driverforge deploy --device <name>` instead of re-selecting it.\n\n" +
			"The name is the one the Anvil UI shows for the controller, so it is\n" +
			"shared with everyone in the organization — not a local nickname.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				name = strings.TrimSpace(args[0])
			}
			switch {
			case clearFlag && name != "":
				return clierr.WithCode(clierr.User("--clear takes no name", "run `driverforge device name --clear` on its own"), codeUsage)
			case !clearFlag && name == "":
				return clierr.WithCode(clierr.User("no name given",
					"run `driverforge device name <name>`, or `--clear` to remove the current one"), codeUsage)
			}
			if err := runDeviceName(cmd.Context(), textOut(cmd), name); err != nil {
				return err
			}
			return emit(cmd, deviceShowOut{Device: currentDeviceOut()}, noText)
		},
	}
	cmd.Flags().BoolVar(&clearFlag, "clear", false, "clear the name; the controller falls back to its hostname")
	return cmd
}

// runDeviceName renames the active device to name (empty clears it), then
// updates the cached context so `device show` and deploy headings reflect the
// new name without a re-select.
func runDeviceName(ctx context.Context, out io.Writer, name string) error {
	org, err := orgctx.Current()
	if err != nil {
		return err
	}
	dev, err := orgctx.CurrentDevice()
	if err != nil {
		return err
	}
	api := driverforgeapi.New()
	if name != "" {
		devices, err := api.ListDevices(ctx, org.ID)
		if err != nil {
			return err
		}
		if err := checkNameAvailable(devices, dev.ID, name); err != nil {
			return err
		}
	}

	var updated driverforgeapi.Device
	werr := ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI), namingLabel(dev.Name, name), func() (string, error) {
		var e error
		updated, e = api.UpdateDeviceName(ctx, org.ID, dev.ID, name)
		if e != nil {
			return "", e
		}
		return namedLabel(dev.Name, updated.DisplayName(), name == ""), nil
	})
	if werr != nil {
		return deviceNameErr(werr, dev, org.Name)
	}

	// The context caches the display name; leaving it stale would show the old
	// name on every deploy heading until the next `device select`.
	dev.Name = updated.DisplayName()
	if err := orgctx.SetCurrentDevice(dev); err != nil {
		return fmt.Errorf("saving device: %w", err)
	}
	return nil
}

func namingLabel(oldName, name string) string {
	if name == "" {
		return fmt.Sprintf("Clearing the name for %s…", oldName)
	}
	return fmt.Sprintf("Naming %s %q…", oldName, name)
}

func namedLabel(oldName, newName string, cleared bool) string {
	if cleared {
		return fmt.Sprintf("Cleared the name for %s — now shown as %s", oldName, newName)
	}
	return fmt.Sprintf("Device %s named %q", oldName, newName)
}

// checkNameAvailable refuses a name already used by ANOTHER controller in the
// org, as its name or its hostname. Both are matched by findDevice, so a
// duplicate would make `--device <name>` silently ambiguous — and picking the
// wrong controller means deploying to the wrong place.
func checkNameAvailable(devices []driverforgeapi.Device, selfID, name string) error {
	for i := range devices {
		d := &devices[i]
		if d.ID == selfID {
			continue // renaming a device to what it already is, is fine
		}
		if strings.EqualFold(d.FriendlyName, name) {
			return clierr.WithCode(clierr.User(
				fmt.Sprintf("another controller in this organization is already named %q", name),
				"pick a different name, or rename that controller first (`driverforge device list`)"), codeNameTaken)
		}
		if strings.EqualFold(d.Hostname, name) {
			return clierr.WithCode(clierr.User(
				fmt.Sprintf("%q is another controller's hostname", name),
				"pick a different name — `--device` matches hostnames too, so this one would be ambiguous"), codeNameTaken)
		}
	}
	return nil
}

// deviceNameErr translates the anticipated rejections. The mutation keys on the
// device's ORG ASSOCIATION id, so a controller registered to the user but not
// yet to the org has no handle to rename and comes back "not found".
func deviceNameErr(err error, dev orgctx.Device, orgName string) error {
	var ge *driverforgeapi.GraphError
	if errors.As(err, &ge) {
		msg := strings.ToLower(ge.Message)
		switch {
		case strings.Contains(msg, "not found"):
			return clierr.WithCode(clierr.WrapT(err, "Device not in this organization",
				fmt.Sprintf("%s isn't registered to %s, so it has no name to set there", dev.Name, orgName),
				"deploy to it once to associate it, or add it in the Anvil UI"), codeDeviceNotFound)
		case strings.Contains(msg, "forbidden") || strings.Contains(msg, "permission") || ge.Code == "FORBIDDEN":
			return clierr.WithCode(clierr.WrapT(err, "Not allowed to rename devices",
				fmt.Sprintf("naming a controller changes it for everyone in %s, which your role can't do", orgName),
				"ask an organization admin to name it, or to grant you edit access"), codeForbidden)
		}
	}
	return err
}
