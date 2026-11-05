package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/agentrelease"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
	"github.com/driverforge/cli/internal/updatecheck"
)

// runAgentUpgrade upgrades the agent on dev to the latest build in the release
// store — invoked from the `driverforge upgrade` picker, where selecting the
// agent row is the consent (upgrades are inherently interactive: they act on a
// connected controller). It deploys under the agent's EXACT installed filename
// (so the agent's self-deploy path engages), sends the checksum, and confirms
// via the self-deploy /healthz poll. Returns nil when already current.
func runAgentUpgrade(ctx context.Context, out io.Writer, dev orgctx.Device) error {
	cl, h, herr := dialAgent(ctx, dev)
	if herr != nil {
		return agentUnreachableErr(dev, herr)
	}

	// The exact installed filename is required: deploying under it is what makes
	// the agent treat this as a self-deploy (clean restart + /healthz poll) rather
	// than rejecting it as not-in-project. Older agents don't report it.
	if h.AgentFilename == "" {
		return clierr.WithCode(clierr.UserT("Agent can't self-upgrade yet",
			fmt.Sprintf("The Anvil Agent on %s is too old to upgrade itself.", controllerLabel(dev, h)),
			"Update it once in Composer Pro; after that, `driverforge upgrade` can take over."), codeAgentOutOfDate)
	}

	rel, err := agentrelease.Latest(ctx, h.AgentFilename)
	if err != nil {
		return agentReleaseErr(err)
	}
	if !updatecheck.IsNewer(rel.Version, h.AgentVersion) {
		fmt.Fprintf(out, "The Anvil Agent on %s is up to date (%s).\n", controllerLabel(dev, h), firstNonEmpty(h.AgentVersion, "unknown"))
		return nil
	}

	// Download + verify (cloud → CLI integrity), then deploy under the exact
	// installed name with the checksum (CLI → agent integrity).
	tmp, err := os.MkdirTemp("", "anvil-agent-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	dest := filepath.Join(tmp, h.AgentFilename)
	sha, err := agentrelease.Download(ctx, rel, dest)
	if err != nil {
		return agentReleaseErr(err)
	}
	// The RUNNING agent processes this upgrade, and agents fail closed on an
	// X-Driver-SHA256 they can't verify — only send it when advertised (see
	// CapabilityChecksum). The download above was already verified against the
	// release manifest.
	if !h.Supports(agent.CapabilityChecksum) {
		sha = ""
	}

	structuralHash, _ := structuralHashOfC4z(dest) // best-effort baseline
	f, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	dctx, cancel := context.WithTimeout(ctx, deployStreamTimeout)
	defer cancel()
	optIn := h.Supports(agent.CapabilityDeployStream)

	if ui.Interactive(flagNoTUI) {
		return runAgentUpgradeTUI(dctx, cl, dev, h.AgentFilename, structuralHash, sha, f, optIn, h.AgentVersion, rel.Version)
	}
	return runAgentUpgradePlain(dctx, out, cl, dev, h.AgentFilename, structuralHash, sha, f, optIn, h.AgentVersion, rel.Version)
}

// runAgentUpgradeTUI streams the agent upgrade through the deploy progress UI.
// The agent's self-deploy path (it can't watch its own restart) is resolved by
// streamDeploy polling /healthz until the version flips — tolerating the restart
// window per note 7.
func runAgentUpgradeTUI(ctx context.Context, cl *agent.Client, dev orgctx.Device, filename, hash, sha string, body io.Reader, optIn bool, oldVersion, expectedVersion string) error {
	// The agent upgrade records no sync baseline (it deploys the agent's own
	// .c4z, not a user driver), so the terminal state is discarded.
	_, err := runDeployProgram(ctx, dev, "Upgrading the Anvil Agent on", expectedVersion, func(send func(agent.DeployEvent)) (agent.DeployEvent, error) {
		return streamDeploy(ctx, cl, dev, filename, hash, sha, body, optIn, oldVersion, send)
	})
	return err
}

func runAgentUpgradePlain(ctx context.Context, out io.Writer, cl *agent.Client, dev orgctx.Device, filename, hash, sha string, body io.Reader, optIn bool, oldVersion, expectedVersion string) error {
	fmt.Fprintf(out, "\nUpgrading the Anvil Agent on %s (%s → %s)…\n", dev.Name, oldVersion, expectedVersion)
	terminal, sErr := streamDeploy(ctx, cl, dev, filename, hash, sha, body, optIn, oldVersion, ui.DeployPlain(out))
	if sErr != nil {
		return friendlyDeployErr(dev, sErr)
	}
	switch terminal.State {
	case agent.StateFailed:
		return deployFailErr(terminal)
	case agent.StateUnconfirmed:
		fmt.Fprintf(out, "  ⚠ %s\n", firstNonEmpty(terminal.Hint, "couldn't confirm the agent restart — re-check shortly"))
	default:
		ui.PrintDeploySummaryPlain(out, firstNonEmpty(terminal.Version, expectedVersion))
	}
	return nil
}

// controllerLabel prefers the agent-reported controller name, falling back to the
// locally-stored device name.
func controllerLabel(dev orgctx.Device, h agent.Health) string {
	return firstNonEmpty(h.ControllerName, dev.Name)
}

// agentReleaseErr renders a release-store / download / integrity failure as a
// friendly card — these are environment conditions (offline, bad build), not CLI
// bugs, so they're never reported. The library's "agentrelease:" origin prefix
// is for crash cards; strip it so the friendly card reads as a sentence.
func agentReleaseErr(err error) error {
	msg := strings.TrimPrefix(err.Error(), "agentrelease: ")
	return clierr.WithCode(clierr.WrapT(err, "Agent update unavailable", capitalise(msg),
		"Check your connection and try again, or update the agent in Composer Pro."), codeUnavailable)
}
