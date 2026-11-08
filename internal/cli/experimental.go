package cli

import (
	"os"
	"strings"
)

// experimental reports whether an unreleased feature is switched on.
// DRIVERFORGE_EXPERIMENTAL is a comma-separated list of feature names, or
// "all". A gated command stays registered, so its path is reserved, but is
// hidden from help and refuses to run until switched on.
func experimental(name string) bool {
	for _, f := range strings.Split(os.Getenv("DRIVERFORGE_EXPERIMENTAL"), ",") {
		if f = strings.TrimSpace(strings.ToLower(f)); f == name || f == "all" {
			return true
		}
	}
	return false
}
