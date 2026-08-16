package errreport

import (
	"runtime"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"

	"github.com/driverforge/cli/internal/buildinfo"
)

var (
	initOnce sync.Once
	initOK   bool
)

// ensureInit lazily initialises the Sentry SDK (only when we're about to
// report). Returns false if there's no DSN or init failed.
func ensureInit() bool {
	initOnce.Do(func() {
		dsn := resolveDSN()
		if dsn == "" {
			return
		}
		err := sentry.Init(sentry.ClientOptions{
			Dsn:         dsn,
			Release:     buildinfo.Version,
			Environment: environment(),
			// Without this, CaptureException events carry only the error
			// string + tags — no stack, no location, nothing to debug with.
			AttachStacktrace: true,
		})
		initOK = err == nil
	})
	return initOK
}

// environment tags released builds as "production" and local/dev builds
// (version "dev") as "development".
func environment() string {
	if buildinfo.Version == "" || buildinfo.Version == "dev" {
		return "development"
	}
	return "production"
}

// Invocation describes the command that failed, for error-report context.
// Positional argument VALUES are never captured — like flag values, they carry
// user data (slugs, device names, paths embedding the username); only the
// count travels.
type Invocation struct {
	Command  string   // full subcommand path, e.g. "org list" (not just "list")
	ArgCount int      // number of positional arguments (values withheld)
	Flags    []string // names of flags that were set (values withheld)
}

// send reports err to Sentry with context tags and returns the event id (empty
// on failure). Best-effort: it flushes with a short timeout and never blocks the
// CLI for long. The `command` tag is the full subcommand path so failures are
// precisely attributable; the arg count and set flag names (never values) go in
// extras. stack, when non-empty, is the panic-time goroutine stack captured in
// the recover (the SDK's own capture would be useful too, but this one is
// guaranteed to include the panic origin frames).
func send(err error, stack []byte, inv Invocation) string {
	if !ensureInit() {
		return ""
	}
	var id *sentry.EventID
	sentry.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("command", orUnknown(inv.Command))
		scope.SetTag("os", runtime.GOOS)
		scope.SetTag("arch", runtime.GOARCH)
		scope.SetTag("driverforge.version", buildinfo.Version)
		details := sentry.Context{}
		if inv.ArgCount > 0 {
			details["argCount"] = inv.ArgCount
		}
		if len(inv.Flags) > 0 {
			details["flags"] = inv.Flags
		}
		if len(details) > 0 {
			scope.SetContext("invocation", details)
		}
		if len(stack) > 0 {
			scope.SetContext("panic", sentry.Context{"stack": string(stack)})
		}
		id = sentry.CaptureException(err)
	})
	sentry.Flush(2 * time.Second)
	if id == nil {
		return ""
	}
	return string(*id)
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
