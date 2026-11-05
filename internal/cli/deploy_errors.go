package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

// Deploy failure conditions, mapped to friendly cards. Split out of deploy.go so
// that file stays under the maintainability gate's file-complexity threshold —
// these are a cohesive set (every one turns a deploy fault into a clierr.UserError
// or decides it is a genuine bug) and read better together than interleaved with
// the deploy flow itself.

// agentDocsURL points at the Anvil Agent install/troubleshooting docs. Published
// contract — see docsBaseURL in usage.go: repoint the slug in Shlink when the docs
// move, because released binaries keep printing this exact URL.
const agentDocsURL = "https://go.driverforge.com/agent"

// classifyDeployRunErr separates the two ways a deploy program dies: the
// stream deadline expiring is a TIMEOUT — the driver may already be staged on
// the controller, so "Cancelled — No changes were made" would be a lie — and
// renders the "Deploy timed out" card; a genuine user abort (ctrl+c, killed
// program without a deadline) gets the friendly Cancelled.
func classifyDeployRunErr(dev orgctx.Device, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return friendlyDeployErr(dev, err)
	}
	return ui.ExpectedIfAborted(err)
}

// deployFailErr is the friendly, non-reportable error for a driver the controller
// rejected or that crashed on init — a user/driver condition, not a CLI bug. It
// links the captured Driverforge error report when the agent provides one.
func deployFailErr(terminal agent.DeployEvent) error {
	msg := firstNonEmpty(terminal.Error, firstNonEmpty(terminal.Step, "the controller rejected the driver"))
	hint := ""
	if terminal.ReportURL != "" {
		hint = "see " + terminal.ReportURL
	}
	return clierr.WithCode(clierr.UserT("Deploy failed", msg, hint), codeDeployFailed)
}

// friendlyDeployErr maps a deploy failure to a friendly error. A timeout and an
// agent-reported rejection (e.g. the driver isn't in the director's project yet)
// are user/setup conditions, not CLI bugs — they render as a card and are never
// reported. Anything else (a genuine transport/decoding fault) stays reportable.
func friendlyDeployErr(dev orgctx.Device, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return clierr.WithCode(clierr.WrapT(err, "Deploy timed out",
			fmt.Sprintf("Deploy to %s timed out — the controller didn't finish in time.", dev.Name),
			"Check the controller and try again — see "+agentDocsURL), codeDeployTimedOut)
	}
	// The agent answered with a reason (a structured error). Whatever it is, the
	// CLI did its job — surface the agent's message, don't crash/report.
	var ae *agent.Error
	if errors.As(err, &ae) {
		// The driver isn't in the director's project yet. Until the agent can
		// bootstrap an install itself (see runBootstrap, gated on the bootstrap
		// capability), guide the user to add it once in Composer Pro — a clean,
		// CLI-owned message rather than the agent's raw wording.
		if ae.Code == agent.CodeNotInProject {
			return clierr.WithCode(clierr.WrapT(err, "Set up in Composer Pro first",
				fmt.Sprintf("The driver isn't part of the project on %s yet, so it can't be deployed.", dev.Name),
				"Add it to a room once in Composer Pro, then run `driverforge deploy` again."), codeNotInProject)
		}
		return clierr.WithCode(clierr.WrapT(err, "Deploy failed", capitalise(ae.Error()), ""), codeDeployFailed)
	}
	return fmt.Errorf("deploy: %w", err)
}

// agentUnreachableErr is the friendly condition when the controller's deploy
// server can't be reached (not installed, not running, crashed on load, blocked,
// or a TLS/cert mismatch). Rendered as a warning card pointing at the agent docs,
// never reported — it's an environment problem, not a CLI bug.
func agentUnreachableErr(dev orgctx.Device, cause error) error {
	// An anticipated, user-facing condition (e.g. the pinned certificate changed)
	// already carries a more specific message and hint than the generic
	// unreachable card — surface it rather than burying it.
	if clierr.IsUser(cause) {
		return cause
	}
	where := dev.Name
	if host := deviceHost(dev); host != "" {
		where = fmt.Sprintf("%s (%s)", dev.Name, host)
	}

	// "Couldn't reach it" is true of every failure here and useful for none of
	// them. The transport error already says WHICH way it failed, and the three
	// ways have nothing in common: a refused connection means the controller is
	// right there and nothing is serving; a timeout means we never got to it at
	// all; an unresolvable name means we were never going to. Each gets the step
	// that actually applies, and the docs carry the why — a hint that explains
	// the agent's internals dates the moment those internals change.
	switch {
	case isConnRefused(cause):
		return clierr.WithCode(clierr.WrapT(cause, "Agent not accepting connections",
			fmt.Sprintf("%s answered, but nothing is listening on the deploy port.", where),
			"In Composer, open the Anvil Agent driver and run its \"Restart Deploy Server\" action — see "+agentDocsURL), codeAgentUnreachable)

	case isTimeout(cause):
		return clierr.WithCode(clierr.WrapT(cause, "Controller not responding",
			fmt.Sprintf("No response from %s.", where),
			"Check the controller is powered on and reachable from this machine — a VLAN or firewall "+
				"between you and it will block the deploy port. If its address has changed, re-run "+
				"`driverforge device select` — see "+agentDocsURL), codeAgentUnreachable)

	case isDNSError(cause):
		return clierr.WithCode(clierr.WrapT(cause, "Controller address won't resolve",
			fmt.Sprintf("Couldn't resolve an address for %s.", where),
			"A Control4 hostname isn't a DNS name, so the controller's IP is what deploy uses. "+
				"Re-run `driverforge device select` to refresh it — see "+agentDocsURL), codeAgentUnreachable)
	}

	return clierr.WithCode(clierr.WrapT(cause, "Agent unreachable",
		fmt.Sprintf("Couldn't reach the Anvil Agent on %s.", where),
		"Check the Anvil Agent is installed and running on the controller — see "+agentDocsURL), codeAgentUnreachable)
}

// isConnRefused reports a TCP refusal: the host is up and answered, but nothing
// is bound to the deploy port. Distinct from a timeout, and the distinction is
// the whole diagnosis — it rules out the network and points at the agent.
func isConnRefused(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}

// isTimeout reports that nothing answered in time — the packets went nowhere, so
// the controller is off, on another network, or behind something dropping them.
// Covers both a net.Error timeout and a context deadline, since a request bounded
// by ctx surfaces the latter.
func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isDNSError reports a name that wouldn't resolve — reachable only when a device
// record has a hostname and no IP, since deviceHost prefers the IP.
func isDNSError(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de)
}

// agentMissingCapabilityErr is the friendly condition when the agent answers but
// is too old to do what we asked.
func agentMissingCapabilityErr(dev orgctx.Device, capability, version string) error {
	msg := fmt.Sprintf("The Anvil Agent on %s doesn't support %s.", dev.Name, capability)
	if version != "" {
		msg = fmt.Sprintf("The Anvil Agent on %s (%s) doesn't support %s.", dev.Name, version, capability)
	}
	return clierr.WithCode(clierr.UserT("Agent out of date", msg, "Update the Anvil Agent on the controller — see "+agentDocsURL), codeAgentOutOfDate)
}
