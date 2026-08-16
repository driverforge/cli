package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/deploycache"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/pkg/c4z/driverxml"
)

// runBootstrap is the first-deploy wizard: when the driver isn't in the
// director's project yet, walk the installer through adding it — choose a
// location from the project tree, name the instance, fill in driver.xml
// properties — then POST /install, which creates the device and streams the same
// progress as a deploy. Interactive only (the caller gates on that).
//
// Declining or cancelling at any step exits cleanly (the user can do it in
// Composer Pro instead); only genuine failures return an error.
func runBootstrap(ctx context.Context, out io.Writer, cl *agent.Client, dev orgctx.Device, c4zPath, filename, hash, localVersion string) error {
	driverXML, err := readC4zEntry(c4zPath, "driver.xml")
	if err != nil {
		return err
	}
	defaultName := driverDisplayName(driverXML, filename)

	// 1. Offer to set it up.
	ok, err := ui.Confirm(
		fmt.Sprintf("%s isn't set up on %s yet — set it up now?", defaultName, dev.Name),
		"This adds the driver to the project and configures it. You can also do this in Composer Pro.",
		true)
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintf(out, "\nNo problem — you can add %s to %s in Composer Pro whenever you're ready.\n", defaultName, dev.Name)
		return nil
	}

	// 2. Fetch the project's location hierarchy.
	roots, err := cl.ProjectTree(ctx)
	if err != nil {
		return bootstrapAgentErr(dev, err)
	}
	nodes := toTreeNodes(roots)
	if !anySelectable(nodes) {
		return clierr.UserT("Nothing to install into",
			fmt.Sprintf("There are no locations on %s that %s can be added to.", dev.Name, defaultName),
			"Add a room in Composer Pro, then deploy again.")
	}

	// 3. Choose a location.
	loc, err := pickLocation(ctx, fmt.Sprintf("Where should %s go?", defaultName), nodes)
	if err != nil {
		return err
	}
	if loc == nil {
		fmt.Fprintln(out, "\nCancelled — nothing was installed.")
		return nil
	}

	// 4. Name the installed driver.
	name, err := ui.PromptText("Name for this driver", defaultName)
	if err != nil {
		return err
	}

	// 5. Fill in properties (driver.xml defaults pre-filled).
	props, err := driverxml.Properties(driverXML)
	if err != nil {
		return err
	}
	values, err := ui.PropertiesForm(name, props)
	if err != nil {
		return err
	}

	// 6. Install + stream progress.
	f, err := os.Open(c4zPath)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintf(out, "\nInstalling %s into %s on %s…\n", name, loc.Name, dev.Name)
	req := agent.InstallRequest{
		Filename:       filename,
		StructuralHash: hash,
		Location:       loc.ID,
		Name:           name,
		Properties:     values,
		C4z:            f,
	}
	return runInstall(ctx, out, cl, dev, req, localVersion)
}

// runInstall streams an install through the deploy progress UI (an install is a
// deploy that also creates the device), and records the baseline on success.
func runInstall(ctx context.Context, out io.Writer, cl *agent.Client, dev orgctx.Device, req agent.InstallRequest, localVersion string) error {
	ictx, cancel := context.WithTimeout(ctx, deployStreamTimeout)
	defer cancel()

	// Subscribe to the driver's telemetry before the install so the OnDriverInit
	// the SDK reports on load isn't missed. Best-effort: a nil watch is a no-op.
	watch := startFirstDeployWatch(ctx, dev)
	defer watch.Close()

	// runBootstrap (the only caller) is gated on an interactive terminal, so the
	// install always streams through the deploy progress UI.
	state, err := runDeployProgram(ictx, dev, "Installing "+req.Name+" to", localVersion, func(send func(agent.DeployEvent)) (agent.DeployEvent, error) {
		_, terminal, sErr := cl.Install(ictx, req, send)
		return terminal, sErr
	})
	if err != nil {
		return err
	}

	// Record the baseline only when the restart was confirmed — an unconfirmed
	// install leaves future syncs without a verified hash rather than a wrong
	// one (see deployConfirmed).
	if deployConfirmed(state) {
		_ = deploycache.Set(dev.ID, req.Filename, req.StructuralHash)
		// Now that the driver is running on the controller, wait for it to check
		// in with Anvil and celebrate the first-ever deploy.
		watch.celebrate(ctx, out, req.Name)
	}
	return nil
}

// pickLocation runs the tree picker and returns the chosen node, or nil if the
// user cancelled.
func pickLocation(ctx context.Context, title string, nodes []ui.TreeNode) (*ui.TreeNode, error) {
	m := ui.NewTreePicker(title, nodes)
	p := tea.NewProgram(m, tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		return nil, ui.ExpectedIfAborted(err)
	}
	if m.Aborted() {
		return nil, nil
	}
	return m.ChosenNode(), nil
}

// toTreeNodes maps the agent's project hierarchy into the UI tree type.
func toTreeNodes(nodes []agent.ProjectNode) []ui.TreeNode {
	out := make([]ui.TreeNode, len(nodes))
	for i, n := range nodes {
		out[i] = ui.TreeNode{
			ID:         n.ID,
			Name:       n.Name,
			Type:       n.Type,
			Selectable: n.Selectable,
			Children:   toTreeNodes(n.Children),
		}
	}
	return out
}

func anySelectable(nodes []ui.TreeNode) bool {
	for _, n := range nodes {
		if n.Selectable || anySelectable(n.Children) {
			return true
		}
	}
	return false
}

// driverDisplayName is the driver's <name> from driver.xml, falling back to the
// artifact filename (without extension) when absent.
func driverDisplayName(driverXML []byte, filename string) string {
	if name, ok := driverxml.ReadName(driverXML); ok {
		if name = strings.TrimSpace(name); name != "" {
			return name
		}
	}
	return strings.TrimSuffix(filepath.Base(filename), ".c4z")
}

// bootstrapAgentErr renders an agent-reported failure during bootstrap as a
// friendly card (the agent answered; surfacing its message is never a CLI bug).
func bootstrapAgentErr(dev orgctx.Device, err error) error {
	if code := agent.Code(err); code != "" {
		return clierr.WrapT(err, "Couldn't set up the driver", capitalise(err.Error()), "")
	}
	return fmt.Errorf("bootstrap on %s: %w", dev.Name, err)
}
