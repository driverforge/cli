package agent

import "github.com/driverforge/cli/internal/sanitize"

// DeployState is the lifecycle state of a streamed deploy. It is the contract —
// branch on this, never on Step (a human display string that may be reworded).
type DeployState string

const (
	StateStaged              DeployState = "staged"                // driver written to the controller
	StateReinstalling        DeployState = "reinstalling"          // director reinstalling the driver
	StateRestarting          DeployState = "restarting"            // driver restarting
	StateConfirmed           DeployState = "confirmed"             // restarted + ready; Version = running build
	StateConfirmedWithErrors DeployState = "confirmed_with_errors" // ready, but its init handler threw (Error set)
	StateFailed              DeployState = "failed"                // init error, never became ready (Error set)
	StateUnconfirmed         DeployState = "unconfirmed"           // no ready signal within the timeout (Hint set)
)

// ReasonSelfDeploy marks a terminal "restarting" event for a self-deploy: the
// agent is torn down by its own reinstall and can't observe its restart, so the
// CLI polls GET /healthz until agentVersion changes rather than waiting.
const ReasonSelfDeploy = "self_deploy"

// DeployEvent is one NDJSON line streamed from the agent's /deploy response. The
// stream ends at the first event with Terminal true. State is the contract; Step
// is a human display string. Optional fields are set per state (see the consts).
//
// ReportURL is a link to the captured Driverforge error report for an init failure, so
// the CLI can point the user straight at remediation. (Proposed addition to the
// agent contract — empty until the agent populates it.)
type DeployEvent struct {
	State     DeployState `json:"state"`
	Step      string      `json:"step"`
	Terminal  bool        `json:"terminal"`
	Version   string      `json:"version,omitempty"`   // confirmed / confirmed_with_errors
	Error     string      `json:"error,omitempty"`     // failed / confirmed_with_errors
	Hint      string      `json:"hint,omitempty"`      // unconfirmed
	Reason    string      `json:"reason,omitempty"`    // self_deploy
	ReportURL string      `json:"reportUrl,omitempty"` // link to the captured Driverforge error report
	Bytes     int         `json:"bytes,omitempty"`     // staged
}

// CapabilityDeployStream is advertised in /healthz capabilities by agents that
// support the streaming deploy protocol; the CLI only opts in when present.
const CapabilityDeployStream = "deploy-stream"

// CapabilityChecksum is advertised in /healthz capabilities by agents that can
// verify an X-Driver-SHA256 upload checksum. Agents fail CLOSED on the header
// when they can't compute a digest (checksum_unsupported: "SHA256 not
// available on this controller"), so the CLI only sends it when advertised —
// mirroring the deploy-stream opt-in. Uploads to agents without it still ride
// the pinned-certificate TLS channel, and release downloads are verified
// against the store manifest CLI-side either way.
const CapabilityChecksum = "checksum"

// CapabilityNavigatorRefresh is advertised in /healthz capabilities by agents
// that implement POST /refresh-navigators, which triggers a Refresh Navigators
// on the controller so navigators re-read cached driver UI schemas
// (protocol 5).
const CapabilityNavigatorRefresh = "refresh-navigators"

// scrub strips terminal control sequences from the agent-supplied display
// strings — they are rendered into the user's terminal, and a hostile peer
// could embed ANSI/OSC sequences to spoof CLI output. Machine fields (State,
// Reason) are compared against constants, never rendered, and stay verbatim.
func (e *DeployEvent) scrub() {
	e.Step = sanitize.Terminal(e.Step)
	e.Version = sanitize.Terminal(e.Version)
	e.Error = sanitize.Terminal(e.Error)
	e.Hint = sanitize.Terminal(e.Hint)
	e.ReportURL = sanitize.Terminal(e.ReportURL)
}
