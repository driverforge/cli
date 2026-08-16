// Package errreport sends unexpected CLI errors to Sentry, opt-in per error.
// Nothing is sent unless the user agrees (or has chosen "always"); expected
// user errors (not logged in, no org/device) are never reported. Reporting is
// disabled by an empty DSN or any of DO_NOT_TRACK / DRIVERFORGE_NO_METRICS /
// DRIVERFORGE_NO_ERROR_REPORTING / CI.
package errreport

import "os"

// SentryDSN is the project's Sentry ingest key. It defaults empty so the CLI
// ships "wired but dark" — a source build (or a fork) never reports anywhere.
// Official releases inject the DSN at build time via -ldflags -X (see
// .goreleaser.yaml), mirroring internal/metrics; a runtime override is
// available via DRIVERFORGE_SENTRY_DSN. Reporting is disabled when it
// resolves to empty. (A DSN is a public client key, not a secret — keeping it
// out of source just prevents accidental telemetry from unofficial builds.)
var SentryDSN = ""

func resolveDSN() string {
	if v := os.Getenv("DRIVERFORGE_SENTRY_DSN"); v != "" {
		return v
	}
	return SentryDSN
}
