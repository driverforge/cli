// Package buildinfo carries the CLI's version metadata, overridden at release
// time via -ldflags (e.g. -X .../buildinfo.Version=1.2.3).
package buildinfo

import "fmt"

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String renders a human-readable version line.
func String() string {
	return fmt.Sprintf("driverforge %s (commit %s, built %s)", Version, Commit, Date)
}
