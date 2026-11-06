package cli

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"unicode"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/driverforgeapi"
	"github.com/driverforge/cli/internal/errreport"
	"github.com/driverforge/cli/internal/telemetryread"
)

// Exit statuses, for every command. A script or agent branches on the status
// (or, in structured output, the error's code) rather than the message.
const (
	exitUsage       = 2   // a bad flag or value, or an answer a prompt would have asked for
	exitWaitTimeout = 3   // a stop condition wasn't met in time
	exitAuth        = 4   // not signed in, or the session expired
	exitContext     = 5   // org, project or device unresolved
	exitRateLimited = 6   // rate limited or a plan limit reached
	exitInterrupted = 130 // SIGINT
)

// Stable error codes, reported in structured output. Messages may change;
// codes don't. A clierr.UserError carries its code (clierr.WithCode); one
// without a code reports codeFailed.
const (
	codeUsage          = "usage_error"
	codeInputRequired  = clierr.CodeInputRequired
	codeAuthRequired   = clierr.CodeAuthRequired
	codeInterrupted    = clierr.CodeInterrupted
	codeRateLimited    = "rate_limited"
	codeConnectionLost = clierr.CodeConnectionLost
	codeUnavailable    = clierr.CodeServiceUnavailable
	codeInternal       = "internal_error"
	codeFailed         = "failed"

	// Scope: what a command acts on.
	codeContextUnresolved = "context_unresolved"
	codeContextAmbiguous  = "context_ambiguous"
	codeOrgNotFound       = "org_not_found"
	codeOrgNotSelected    = clierr.CodeOrgNotSelected
	codeProjectNotFound   = "project_not_found"
	codeDeviceNotFound    = "device_not_found"
	codeDeviceNotSelected = clierr.CodeDeviceNotSelected
	codeNoDevices         = "no_devices"
	codeNotAProject       = "not_a_project"
	codeNotInitialised    = "not_initialised"
	codeInvalidConfig     = clierr.CodeInvalidConfig
	codeNameTaken         = "name_taken"
	// doctor
	codeRepairUnavailable = "repair_unavailable"
	// init
	codeProjectLimit          = "project_limit_reached"
	codeSubscriptionSuspended = "subscription_suspended"
	codeRepairPending         = "repair_pending" // a warning: repairs listed but not written
	codeForbidden             = "forbidden"

	// Telemetry readers.
	codeEntryNotFound = "entry_not_found"

	// Building and shipping a driver.
	codeBuildFailed        = "build_failed"
	codeBuildWarning       = "build_warning" // a warning, not a failure
	codeVersioning         = "versioning_not_configured"
	codeAgentUnreachable   = "agent_unreachable"
	codeAgentOutOfDate     = "agent_out_of_date"
	codeCertificateChanged = clierr.CodeCertificateChanged
	codeDeployFailed       = "deploy_failed"
	codeDeployTimedOut     = "deploy_timed_out"
	codeNotInProject       = "driver_not_in_project"
	codeSyncFailed         = "sync_failed"
	codeDeployRequired     = "deploy_required"

	// Live debugging.
	codeRuntimeUnavailable = "runtime_unavailable"

	// Agent skills.
	codeSkillNotFound = "skill_not_found"
	codeSkillConflict = "skill_conflict"

	// Commands that can't run without a person.
	codeInteractiveRequired = "interactive_required"
	codeNotAvailable        = "not_available"
)

// codeExit is the exit status for a code, where it isn't 1.
var codeExit = map[string]int{
	codeUsage:             exitUsage,
	codeInputRequired:     exitUsage,
	codeAuthRequired:      exitAuth,
	codeContextUnresolved: exitContext,
	codeContextAmbiguous:  exitContext,
	codeOrgNotFound:       exitContext,
	codeOrgNotSelected:    exitContext,
	codeProjectNotFound:   exitContext,
	codeDeviceNotFound:    exitContext,
	codeDeviceNotSelected: exitContext,
	codeNoDevices:         exitContext,
	codeNotAProject:       exitContext,
	codeNotInitialised:    exitContext,
	codeRateLimited:       exitRateLimited,
	codeProjectLimit:      exitRateLimited,
	codeInterrupted:       exitInterrupted,
}

// cmdErr is a failure in its published shape. Message and hint are written for
// the reader and never carry internals (hosts, services, raw server errors);
// details holds structured facts such as the missing field or the candidates
// to choose from.
type cmdErr struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Hint      string         `json:"hint,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
	Reference string         `json:"reference,omitempty"`

	cause error
	// status overrides the code's exit status (a UserError's own Exit).
	status int
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
	if e.status != 0 {
		return e.status
	}
	if c, ok := codeExit[e.Code]; ok {
		return c
	}
	return 1
}

// failureText is what describe says about failures it can't otherwise name.
type failureText struct {
	internal  string // an unexpected failure
	rateLimit string // the hint for a rate-limited request
}

var (
	// commandFailure is the wording for any command.
	commandFailure = failureText{
		internal:  "Something went wrong.",
		rateLimit: "Wait a moment and retry.",
	}
	// readFailure is the telemetry readers' wording, part of their contract.
	readFailure = failureText{
		internal:  "Something went wrong reading telemetry.",
		rateLimit: "Wait and retry, or narrow the query with --since or --tail.",
	}
)

// classify maps a telemetry reader's failure to a stable code.
func classify(err error) *cmdErr { return describe(err, readFailure) }

// describe maps any failure to a stable code. Anything not anticipated becomes
// internal_error, reported (when the user has agreed to error reports) so its
// reference can be looked up; the raw error never reaches the output.
func describe(err error, text failureText) *cmdErr {
	var ce *cmdErr
	var ue *clierr.UserError
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
			Hint: text.rateLimit, cause: err}
		if rl.RetryAfter > 0 {
			e.with("retry_after_ms", rl.RetryAfter.Milliseconds())
		}
		return e
	case errors.Is(err, telemetryread.ErrConnectionLost):
		return &cmdErr{Code: codeConnectionLost, Message: "The live connection to Driverforge dropped and didn't come back within a minute.",
			Hint: "Check your connection and re-run; entries already printed are complete up to the drop.", cause: err}
	case errors.As(err, &ue):
		return &cmdErr{Code: firstNonEmpty(ue.Code, codeFailed), Message: sentence(ue.Message),
			Hint: sentence(ue.Hint), cause: err, status: ue.Exit}
	case errors.As(err, &urlErr), errors.As(err, &netErr):
		return &cmdErr{Code: codeConnectionLost, Message: "Driverforge couldn't be reached.",
			Hint: "Check your connection and try again.", cause: err}
	}
	e := &cmdErr{Code: codeInternal, Message: text.internal,
		Hint: "Try again; if it persists, contact support with the reference.", cause: err}
	e.Reference = errreport.ReportQuietly(err, lastInvocation)
	return e
}

// sentence makes a UserError's message fragment ("no device is selected")
// read as the sentence structured output promises: capitalised, unless it
// opens with a name that must keep its case (a file, a flag, the command), and
// ending in a full stop.
func sentence(s string) string {
	if s == "" {
		return ""
	}
	first, _, _ := strings.Cut(s, " ")
	if first != "driverforge" && !strings.ContainsFunc(first, func(r rune) bool {
		return !unicode.IsLetter(r) && r != '\'' && r != '’'
	}) {
		s = capitalise(s)
	}
	switch s[len(s)-1] {
	case '.', '!', '?', ')', '`':
		return s
	}
	return s + "."
}

// exitFor is the process exit status for a failure: a UserError's own Exit,
// else its code's, else 1.
func exitFor(err error) int {
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		return 1
	}
	if ue.Exit != 0 {
		return ue.Exit
	}
	if c, ok := codeExit[ue.Code]; ok {
		return c
	}
	return 1
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
