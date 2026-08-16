// Package auth implements `driverforge login` — an OAuth2 Device Authorization Grant
// (RFC 8628) against ORY Hydra — plus secure storage of the resulting tokens.
//
// The CLI is distributed to third parties, so it is treated as a public,
// zero-trust client: it never sees user credentials (the user authorizes in
// their own browser) and holds no client secret. Tokens are stored in the OS
// secret store when available, falling back to a 0600 file.
package auth

import (
	"net/url"
	"os"
	"strings"

	"golang.org/x/oauth2"
)

// Endpoint/client configuration. Defaults target production and are overridable
// at build time via -ldflags -X (e.g.
// -X github.com/driverforge/cli/internal/auth.AuthURL=...) and at runtime
// via the DRIVERFORGE_* env vars below — mirroring internal/metrics/endpoints.go.
var (
	// AuthURL is the Hydra public base URL (the OAuth2 endpoints live under it).
	// Dev: http://localhost:4444. The issuer claim in dev is auth.driverforge.test.
	AuthURL = "https://auth.driverforge.com"
	// ClientID is the public device-flow client registered in Hydra.
	ClientID = "driverforge-cli"
	// Audience is the Graph API audience (Hydra `audience`).
	Audience = "graph"
	// AgentAudience is the controller deploy-server audience. The agent requires
	// it, and we mint an agent-only token (refresh-downgrade) for agent calls so
	// a token that reaches the controller can't be replayed against graph.
	// Deliberately still the original "anvil-agent": renaming it means updating the
	// deployed agents and Hydra together, deferred from the CLI rename.
	AgentAudience = "anvil-agent"
	// GraphScope is the OAuth scope the Graph API requires on every request — it
	// fails closed without it. Requested at login so the (union) access token
	// used for graph calls carries it (Hydra `scope`, distinct from the `graph`
	// audience). Agent tokens are narrowed on refresh and deliberately omit it.
	GraphScope = "graph"
)

// resolveAuthURL applies the env override and trims any trailing slash.
func resolveAuthURL() string {
	u := AuthURL
	if v := os.Getenv("DRIVERFORGE_AUTH_URL"); v != "" {
		u = v
	}
	return strings.TrimRight(u, "/")
}

func resolveClientID() string {
	if v := os.Getenv("DRIVERFORGE_AUTH_CLIENT_ID"); v != "" {
		return v
	}
	return ClientID
}

func resolveAudience() string {
	if v := os.Getenv("DRIVERFORGE_AUTH_AUDIENCE"); v != "" {
		return v
	}
	return Audience
}

func resolveAgentAudience() string {
	if v := os.Getenv("DRIVERFORGE_AGENT_AUDIENCE"); v != "" {
		return v
	}
	return AgentAudience
}

func resolveGraphScope() string {
	if v := os.Getenv("DRIVERFORGE_AUTH_GRAPH_SCOPE"); v != "" {
		return v
	}
	return GraphScope
}

// baseScopes are the OIDC scopes for login and narrowed refresh grants;
// offline_access yields the refresh token that per-call downgrades mint from.
func baseScopes() []string {
	return []string{"openid", "profile", "email", "offline_access"}
}

// loginScopes are the scopes requested at login: the OIDC base plus the Graph
// API scope, so the union access token can call graph (which fails closed
// without it). Agent tokens are narrowed on refresh and omit the graph scope.
func loginScopes() []string {
	s := baseScopes()
	if g := resolveGraphScope(); g != "" {
		s = append(s, g)
	}
	return s
}

// loginAudiences is the space-separated union requested at login. Hydra requires
// the initial grant to cover every audience we may later downgrade to, so login
// claims both graph and the agent; per-call tokens narrow from there.
func loginAudiences() string {
	auds := resolveAudience()
	if a := resolveAgentAudience(); a != "" && a != resolveAudience() {
		auds = strings.TrimSpace(auds + " " + a)
	}
	return auds
}

// deviceAuthURL, tokenURL and revokeURL are Hydra's stable OAuth2 paths.
func deviceAuthURL() string { return resolveAuthURL() + "/oauth2/device/auth" }
func tokenURL() string      { return resolveAuthURL() + "/oauth2/token" }
func revokeURL() string     { return resolveAuthURL() + "/oauth2/revoke" }

// authHost is the host portion of the auth URL, used as the credential store
// account so multiple environments (prod/dev) can coexist.
func authHost() string {
	if u, err := url.Parse(resolveAuthURL()); err == nil && u.Host != "" {
		return u.Host
	}
	return resolveAuthURL()
}

// InsecureAuthURL reports whether the resolved auth endpoint would send the
// refresh token over cleartext http:// to a non-local host. http://localhost is
// the sanctioned dev setup and is not flagged; an http:// URL pointing anywhere
// else means the refresh token (a long-lived bearer credential) is POSTed in the
// clear. Callers surface this as a warning rather than a hard failure so a
// deliberate lab/proxy setup still works.
func InsecureAuthURL() bool {
	u, err := url.Parse(resolveAuthURL())
	if err != nil || u.Scheme != "http" {
		return false
	}
	return !isLocalHost(u.Hostname())
}

func isLocalHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	// RFC 6761 reserves .localhost and .test for local/testing use — they never
	// resolve on the public internet, so http to them is a dev setup (the dev
	// issuer is auth.driverforge.test), not an exposed cleartext credential.
	return strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".test")
}

// oauthConfig builds the oauth2.Config for the device grant and refresh.
func oauthConfig() *oauth2.Config {
	return &oauth2.Config{
		ClientID: resolveClientID(),
		Scopes:   loginScopes(),
		Endpoint: oauth2.Endpoint{
			DeviceAuthURL: deviceAuthURL(),
			TokenURL:      tokenURL(),
			AuthStyle:     oauth2.AuthStyleInParams,
		},
	}
}
