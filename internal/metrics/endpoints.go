package metrics

import "os"

// Endpoint/token configuration. These default empty so the CLI ships "wired but
// dark"; they are set at release time via -ldflags -X (e.g.
// -X github.com/driverforge/cli/internal/metrics.MixpanelToken=...), and
// can be overridden at runtime via the DRIVERFORGE_* env vars below.
var (
	// MixpanelToken is the Mixpanel project token. Empty => the Mixpanel sink is inert.
	MixpanelToken = ""
	// MixpanelURL is the Mixpanel track endpoint.
	MixpanelURL = "https://api.mixpanel.com/track"
)

// resolveEndpoints applies env-var overrides on top of the build-time defaults.
func resolveEndpoints() (mixToken, mixURL string) {
	mixToken, mixURL = MixpanelToken, MixpanelURL
	if v := os.Getenv("DRIVERFORGE_MIXPANEL_TOKEN"); v != "" {
		mixToken = v
	}
	if v := os.Getenv("DRIVERFORGE_MIXPANEL_URL"); v != "" {
		mixURL = v
	}
	return
}

// defaultSinks builds the sinks from the resolved endpoint configuration. A
// sink with no token is inert (its Send is a no-op).
func defaultSinks() []Sink {
	mixToken, mixURL := resolveEndpoints()
	return []Sink{
		&mixpanelSink{token: mixToken, url: mixURL},
	}
}
