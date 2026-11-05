package errreport

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"golang.org/x/term"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/metrics"
	"github.com/driverforge/cli/internal/ui"
)

// Reportable reports whether err is an unexpected error worth sending to Sentry.
// The rule is purely type-driven: an anticipated, user-facing condition is wrapped
// in a clierr.UserError (rendered as a friendly card, never reported); anything
// else that reaches the top is treated as a bug. The auth/org/device sentinels and
// the per-command "run `driverforge …`" conditions are all UserErrors now, so no
// string- or sentinel-matching fallbacks are needed.
func Reportable(err error) bool {
	if err == nil {
		return false
	}
	return !clierr.IsUser(err)
}

// optedOut mirrors metrics.optedOut, plus a dedicated DRIVERFORGE_NO_ERROR_REPORTING.
func optedOut() bool {
	return os.Getenv("DRIVERFORGE_NO_ERROR_REPORTING") != "" ||
		os.Getenv("DRIVERFORGE_NO_METRICS") != "" ||
		os.Getenv("DO_NOT_TRACK") != "" ||
		os.Getenv("CI") != ""
}

func enabled() bool { return resolveDSN() != "" && !optedOut() }

func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// Handle offers to report a returned command error to Sentry, honouring the
// persisted always/never choice. out receives the prompt/confirmation.
func Handle(err error, inv Invocation, out io.Writer) {
	if !enabled() || !Reportable(err) {
		return
	}
	dispatch(err, nil, inv, out)
}

// ReportQuietly reports err only when the user has already agreed to error
// reporting, and never prompts or prints. It returns the report's reference, or
// "" when nothing was sent. For machine-readable output, where a prompt or a
// stray line would corrupt the stream and the reference is carried in the
// structured error instead.
func ReportQuietly(err error, inv Invocation) string {
	if !enabled() || !Reportable(err) || metrics.Load().ErrorReportingConsent != metrics.ConsentGranted {
		return ""
	}
	return send(err, nil, inv)
}

// ReportPanicQuietly is ReportQuietly for a recovered panic. Like HandlePanic
// it must be called from inside the recovering deferred function, so the
// stack still shows where the panic started.
func ReportPanicQuietly(recovered any, inv Invocation) string {
	if !enabled() || metrics.Load().ErrorReportingConsent != metrics.ConsentGranted {
		return ""
	}
	return send(fmt.Errorf("panic: %v", recovered), debug.Stack(), inv)
}

// HandlePanic reports a recovered panic (always a candidate — it's a crash).
// It must be called from inside the recovering deferred function: at that
// point the panicking frames are still live (deferred functions run from
// runtime.gopanic), so debug.Stack() captures the panic origin — the report
// would otherwise carry no location at all.
func HandlePanic(recovered any, inv Invocation, out io.Writer) {
	if !enabled() {
		return
	}
	dispatch(fmt.Errorf("panic: %v", recovered), debug.Stack(), inv, out)
}

// dispatch applies the consent decision and reports if allowed. stack is the
// panic-time goroutine stack (nil for ordinary errors).
func dispatch(err error, stack []byte, inv Invocation, out io.Writer) {
	switch metrics.Load().ErrorReportingConsent {
	case metrics.ConsentDenied:
		return
	case metrics.ConsentGranted:
		report(err, stack, inv, out)
		return
	}
	if !interactive() {
		return // can't ask; don't send
	}
	switch promptChoice() {
	case "send":
		report(err, stack, inv, out)
	case "always":
		persist(metrics.ConsentGranted)
		report(err, stack, inv, out)
	case "never":
		persist(metrics.ConsentDenied)
	}
}

func report(err error, stack []byte, inv Invocation, out io.Writer) {
	if id := send(err, stack, inv); id != "" {
		fmt.Fprintf(out, "Reported. Reference: %s\n", id)
	}
}

func persist(state string) {
	cfg := metrics.Load()
	cfg.ErrorReportingConsent = state
	_ = metrics.Save(cfg)
}

func promptChoice() string {
	choice, err := ui.PickOne("Report this error to help us fix it?", []ui.Choice{
		{Label: "Send", Value: "send"},
		{Label: "Don't send", Value: "no"},
		{Label: "Always send", Value: "always"},
		{Label: "Never ask again", Value: "never"},
	}, "send")
	if err != nil {
		return "no"
	}
	return choice
}
