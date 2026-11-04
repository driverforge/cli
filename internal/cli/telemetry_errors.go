package cli

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/errreport"
	"github.com/driverforge/cli/internal/telemetryread"
)

// Exit statuses. Every command exits 1 on failure unless it has a documented
// contract; the telemetry readers (logs, errors, events, get) use the full set
// so a script or agent can branch on the status rather than the message.
const (
	exitUsage       = 2   // a bad flag or value
	exitWaitTimeout = 3   // a stop condition wasn't met in time
	exitAuth        = 4   // not signed in, or the session expired
	exitContext     = 5   // scope unresolved or ambiguous, or no deploy recorded
	exitRateLimited = 6   // rate limited or a plan limit reached
	exitInterrupted = 130 // SIGINT
)

// Stable error codes of the telemetry readers. Messages may change; codes
// don't.
const (
	codeUsage             = "usage_error"
	codeAuthRequired      = "auth_required"
	codeContextUnresolved = "context_unresolved"
	codeContextAmbiguous  = "context_ambiguous"
	codeNoDeployRecorded  = "no_deploy_recorded"
	codeProjectNotFound   = "project_not_found"
	codeDeviceNotFound    = "device_not_found"
	codeEntryNotFound     = "entry_not_found"
	codeRateLimited       = "rate_limited"
	codeConnectionLost    = "connection_lost"
	codeInterrupted       = "interrupted"
	codeInternal          = "internal_error"
)

var codeExit = map[string]int{
	codeUsage:             exitUsage,
	codeAuthRequired:      exitAuth,
	codeContextUnresolved: exitContext,
	codeContextAmbiguous:  exitContext,
	codeNoDeployRecorded:  exitContext,
	codeProjectNotFound:   exitContext,
	codeDeviceNotFound:    exitContext,
	codeRateLimited:       exitRateLimited,
	codeInterrupted:       exitInterrupted,
}

// cmdErr is a telemetry reader's failure in its published shape. Message and
// hint are written for the reader and never carry internals (hosts, services,
// raw server errors); details holds structured facts such as the missing field
// or the candidates to choose from.
type cmdErr struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Hint      string         `json:"hint,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Reference string         `json:"reference,omitempty"`

	cause error
}

func newCmdErr(code, message, hint string) *cmdErr {
	return &cmdErr{Code: code, Message: message, Hint: hint}
}

func (e *cmdErr) with(key string, value any) *cmdErr {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = value
	return e
}

func (e *cmdErr) Error() string { return e.Code + ": " + e.Message }
func (e *cmdErr) Unwrap() error { return e.cause }

func (e *cmdErr) exit() int {
	if c, ok := codeExit[e.Code]; ok {
		return c
	}
	return 1
}

// classify maps any failure to a stable code. Anything not anticipated becomes
// internal_error, reported (when the user has agreed to error reports) so its
// reference can be looked up; the raw error never reaches the output.
func classify(err error) *cmdErr {
	var ce *cmdErr
	var rl *driverforgeapi.RateLimitError
	var urlErr *url.Error
	var netErr *net.OpError
	switch {
	case errors.As(err, &ce):
		return ce
	case errors.Is(err, context.Canceled):
		return &cmdErr{Code: codeInterrupted, Message: "Interrupted.", cause: err}
	case errors.Is(err, auth.ErrNotLoggedIn), errors.Is(err, auth.ErrSessionExpired):
		return &cmdErr{Code: codeAuthRequired, Message: "You're not signed in, or your session has expired.",
			Hint: "Run `driverforge login`, then retry.", cause: err}
	case errors.As(err, &rl):
		e := &cmdErr{Code: codeRateLimited, Message: "Driverforge is rate limiting these requests.",
			Hint: "Wait and retry, or narrow the query with --since or --tail.", cause: err}
		if rl.RetryAfter > 0 {
			e.with("retry_after_ms", rl.RetryAfter.Milliseconds())
		}
		return e
	case errors.Is(err, telemetryread.ErrConnectionLost):
		return &cmdErr{Code: codeConnectionLost, Message: "The live connection to Driverforge dropped and didn't come back within a minute.",
			Hint: "Check your connection and re-run; entries already printed are complete up to the drop.", cause: err}
	case errors.As(err, &urlErr), errors.As(err, &netErr):
		return &cmdErr{Code: codeConnectionLost, Message: "Driverforge couldn't be reached.",
			Hint: "Check your connection and try again.", cause: err}
	}
	e := &cmdErr{Code: codeInternal, Message: "Something went wrong reading telemetry.",
		Hint: "Try again; if it persists, contact support with the reference.", cause: err}
	e.Reference = errreport.ReportQuietly(err, lastInvocation)
	return e
}

// asUserError is the cmdErr for text output: the friendly card, with the
// contract's exit status.
func (e *cmdErr) asUserError() error {
	ue := clierr.WrapT(e, "", e.Message, e.Hint)
	ue.Exit = e.exit()
	return ue
}

// silentExit ends the command with status code having already reported why.
func silentExit(code int) error {
	ue := clierr.Silent()
	ue.Exit = code
	return ue
}
