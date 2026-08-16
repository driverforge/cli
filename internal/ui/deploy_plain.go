package ui

import (
	"fmt"
	"io"

	"github.com/driverforge/cli/internal/agent"
)

// DeployPlain returns a deploy-event callback that streams plain progress lines
// to w, for non-interactive output (CI, pipes, --no-tui). Terminal events are
// handled by the caller (which prints the ✓/⚠/✗ outcome + summary).
func DeployPlain(w io.Writer) func(agent.DeployEvent) {
	return func(e agent.DeployEvent) {
		if e.Terminal || e.Step == "" {
			return
		}
		fmt.Fprintf(w, "  → %s…\n", e.Step)
	}
}

// PrintDeploySummaryPlain prints the final version line.
func PrintDeploySummaryPlain(w io.Writer, version string) {
	fmt.Fprintf(w, "  ✓ %s\n", versionDeployedLabel(version))
}
