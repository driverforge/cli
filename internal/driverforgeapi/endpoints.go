// Package driverforgeapi is a thin, authenticated client for the Driverforge Graph
// (GraphQL) API. It obtains a bearer token from internal/auth and hand-rolls
// GraphQL-over-HTTP (no GraphQL dependency).
package driverforgeapi

import (
	"os"
	"strings"
)

// GraphURL is the Graph API endpoint. The default targets production and is
// overridable at build time via -ldflags -X and at runtime via DRIVERFORGE_GRAPH_URL
// (dev: https://graph.driverforge.test/graphql) — mirroring internal/auth.
var GraphURL = "https://graph.driverforge.com/graphql"

func resolveGraphURL() string {
	u := GraphURL
	if v := os.Getenv("DRIVERFORGE_GRAPH_URL"); v != "" {
		u = v
	}
	return strings.TrimRight(u, "/")
}
