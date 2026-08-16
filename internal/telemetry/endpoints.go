package telemetry

import (
	"net/url"
	"os"
	"strings"
)

// Endpoint configuration. Defaults target production and are overridable at
// build time via -ldflags -X and at runtime via the DRIVERFORGE_* env vars below —
// mirroring internal/auth and internal/driverforgeapi.
var (
	// CentrifugoURL is the Centrifugo WebSocket endpoint the web app also
	// connects to. Dev: ws://localhost:8086/connection/websocket.
	CentrifugoURL = "wss://ws.driverforge.dev/connection/websocket"
	// AppURL is the Anvil web app base, used to derive the Centrifugo origin. Dev:
	// https://app.driverforge.test.
	AppURL = "https://app.driverforge.dev"
	// ShortLinkURL is the short-link domain fronting user-facing deep links, whose
	// slugs redirect via the app's /a handler. Empty in source on purpose — the
	// same "wired but dark" pattern as the Sentry DSN and Mixpanel token: only
	// release builds carry it (injected by .goreleaser.yaml), so local, dev and
	// fork builds link straight at the app and never depend on a /a route their
	// stack may not serve. Set DRIVERFORGE_SHORTLINK_URL to exercise it locally,
	// or to "" to force a release binary back to direct links.
	ShortLinkURL = ""
)

func resolveCentrifugoURL() string {
	if v := os.Getenv("DRIVERFORGE_CENTRIFUGO_URL"); v != "" {
		return v
	}
	return CentrifugoURL
}

// ShortLinkBase is the short-link base for user-facing deep links, or "" when
// links should point straight at the app (local, dev and fork builds). AppBase is
// the web app root with any override applied. Both are exported for other packages
// building user-facing links — this package owns the web-app endpoint config, as
// internal/auth and internal/driverforgeapi own theirs.
func ShortLinkBase() string { return resolveShortLinkURL() }

// AppBase is the web app root, no trailing slash.
func AppBase() string { return resolveAppURL() }

// resolveShortLinkURL returns the short-link base, or "" when links should point
// straight at the app. LookupEnv (not Getenv) so an explicitly empty override can
// switch a release binary back to direct links for debugging.
func resolveShortLinkURL() string {
	u := ShortLinkURL
	if v, ok := os.LookupEnv("DRIVERFORGE_SHORTLINK_URL"); ok {
		u = v
	}
	return strings.TrimRight(u, "/")
}

func resolveAppURL() string {
	u := AppURL
	if v := os.Getenv("DRIVERFORGE_APP_URL"); v != "" {
		u = v
	}
	return strings.TrimRight(u, "/")
}

// resolveCentrifugoOrigin is the Origin header the CLI presents on the Centrifugo
// WebSocket handshake. Centrifugo restricts connections by Origin (allowed_origins)
// to protect browsers from CSRF; a headless client sends none and would be
// refused, so the CLI presents the web app's origin — the same one browsers use.
// Overridable via DRIVERFORGE_CENTRIFUGO_ORIGIN.
func resolveCentrifugoOrigin() string {
	if v := os.Getenv("DRIVERFORGE_CENTRIFUGO_ORIGIN"); v != "" {
		return v
	}
	if u, err := url.Parse(resolveAppURL()); err == nil && u.Scheme != "" && u.Host != "" {
		return u.Scheme + "://" + u.Host
	}
	return ""
}
