package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Hydra grants the union of audiences at login; per-call tokens are narrowed
// from the refresh token. We cache narrowed tokens in-process (keyed by
// audience) so repeated calls in one invocation don't re-refresh.
var (
	audMu    sync.Mutex
	audCache = map[string]cachedTok{}
)

type cachedTok struct {
	token  string
	expiry time.Time
}

// AgentAccessToken returns an access token scoped to the controller deploy-server
// audience only (so it can't be replayed against graph).
func AgentAccessToken(ctx context.Context) (string, error) {
	return AccessTokenForAudience(ctx, resolveAgentAudience())
}

// MQTTAccessToken returns an access token scoped to the Anvil broker audience
// only. A session signed in before the broker audience was requested at login
// cannot narrow to it; the grant then fails and the caller should ask for a
// fresh `driverforge login`.
func MQTTAccessToken(ctx context.Context) (string, error) {
	return AccessTokenForAudience(ctx, resolveMQTTAudience())
}

// AccessTokenForAudience returns an access token narrowed to the given audience,
// minted from the stored refresh token (Hydra downgrades audience on refresh).
// If there is no refresh token to narrow from, it falls back to the stored
// (union) access token. The rotated refresh token is persisted; the stored
// union access token is left intact for graph calls.
func AccessTokenForAudience(ctx context.Context, audience string) (string, error) {
	if t, ok := cachedAudienceToken(audience); ok {
		return t, nil
	}
	c, err := Load()
	if err != nil {
		return "", err
	}
	// A token minted by an earlier invocation is on disk — reuse it while valid
	// rather than paying the ~800ms grant again. This is the cross-process win:
	// only the first deploy/sync after login (or after expiry) grants.
	if t, exp, ok := persistedAudienceToken(c, audience); ok {
		cacheAudienceToken(audience, t, exp)
		return t, nil
	}
	if c.RefreshToken == "" {
		return c.AccessToken, nil // can't downgrade — use the token as-is
	}

	// This grant rotates the refresh token, so serialise it across processes
	// (see lockCredentials) and re-load under the lock to use the newest one —
	// a concurrent invocation may have just rotated it.
	release, _ := lockCredentials()
	defer release()
	if fresh, lerr := Load(); lerr == nil && fresh.RefreshToken != "" {
		c = fresh
	}
	if t, ok := cachedAudienceToken(audience); ok {
		return t, nil // filled in by the process we waited on
	}
	if t, exp, ok := persistedAudienceToken(c, audience); ok {
		cacheAudienceToken(audience, t, exp) // another process granted while we waited
		return t, nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.RefreshToken)
	form.Set("client_id", resolveClientID())
	// Narrow to the OIDC base scopes only — the agent token must not carry the
	// graph scope (defence in depth alongside the audience downgrade).
	form.Set("scope", strings.Join(baseScopes(), " "))
	if audience != "" {
		form.Set("audience", audience)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		// Same classification as session.refreshError: an expired/revoked grant
		// is an anticipated condition, not a CLI bug.
		var oe struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(body, &oe)
		cause := fmt.Errorf("token downgrade for audience %q: HTTP %d %s", audience, resp.StatusCode, oe.Error)
		if oe.Error == "invalid_grant" {
			return "", sessionExpiredError(cause)
		}
		return "", signInUnavailableError(cause)
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", signInUnavailableError(fmt.Errorf("token downgrade: %w", err))
	}
	if tok.AccessToken == "" {
		return "", signInUnavailableError(fmt.Errorf("token downgrade for audience %q: empty access_token", audience))
	}

	// Persist the rotated refresh token (keep the union access token for graph)
	// and the freshly minted audience token, in one write. The rotated refresh
	// token is critical — the old one is spent server-side, so a failed save
	// would brick the next refresh — whereas the audience token is a cache, so a
	// failed save there only costs a re-grant next time. Distinguish the two.
	expiry := time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	rotated := tok.RefreshToken != "" && tok.RefreshToken != c.RefreshToken
	if rotated {
		c.RefreshToken = tok.RefreshToken
	}
	if c.AudienceTokens == nil {
		c.AudienceTokens = map[string]AudienceToken{}
	}
	c.AudienceTokens[audience] = AudienceToken{AccessToken: tok.AccessToken, Expiry: expiry}
	if err := Save(c); err != nil && rotated {
		return "", signInUnavailableError(fmt.Errorf("saving rotated refresh token: %w", err))
	}

	cacheAudienceToken(audience, tok.AccessToken, expiry)
	return tok.AccessToken, nil
}

// persistedAudienceToken returns a cross-process-persisted audience token from c
// when present and still comfortably valid (same expiry margin as the in-memory
// cache), so callers can skip the grant.
func persistedAudienceToken(c Credentials, audience string) (string, time.Time, bool) {
	at, ok := c.AudienceTokens[audience]
	if !ok || at.AccessToken == "" || time.Until(at.Expiry) < refreshWindow {
		return "", time.Time{}, false
	}
	return at.AccessToken, at.Expiry, true
}

func cachedAudienceToken(audience string) (string, bool) {
	audMu.Lock()
	defer audMu.Unlock()
	c, ok := audCache[audience]
	if !ok || time.Until(c.expiry) < 30*time.Second {
		return "", false
	}
	return c.token, true
}

func cacheAudienceToken(audience, token string, expiry time.Time) {
	audMu.Lock()
	defer audMu.Unlock()
	audCache[audience] = cachedTok{token: token, expiry: expiry}
}
