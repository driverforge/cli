package auth

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/clierr"
)

func resetAudCache() {
	audMu.Lock()
	audCache = map[string]cachedTok{}
	audMu.Unlock()
}

func isolate(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	resetAudCache()
}

func TestAccessTokenForAudienceDowngrade(t *testing.T) {
	isolate(t)

	var (
		mu                      sync.Mutex
		hits                    int
		gotGrant, gotAud, gotRT string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mu.Lock()
		hits++
		gotGrant, gotAud, gotRT = r.Form.Get("grant_type"), r.Form.Get("audience"), r.Form.Get("refresh_token")
		mu.Unlock()
		io.WriteString(w, `{"access_token":"agent-tok","refresh_token":"rt1","expires_in":1800}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)

	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	tok, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	if err != nil {
		t.Fatalf("AccessTokenForAudience: %v", err)
	}
	if tok != "agent-tok" {
		t.Errorf("token = %q, want agent-tok", tok)
	}
	mu.Lock()
	if gotGrant != "refresh_token" || gotAud != "anvil-agent" || gotRT != "rt0" {
		t.Errorf("downgrade request wrong: grant=%q aud=%q rt=%q", gotGrant, gotAud, gotRT)
	}
	mu.Unlock()

	// Rotated refresh token persisted; the union access token is left intact.
	c, _ := Load()
	if c.RefreshToken != "rt1" {
		t.Errorf("refresh token not rotated: %q", c.RefreshToken)
	}
	if c.AccessToken != "union-tok" {
		t.Errorf("union access token must be preserved, got %q", c.AccessToken)
	}

	// Second call is served from the in-process cache (no extra refresh).
	if _, err := AccessTokenForAudience(context.Background(), "anvil-agent"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("expected 1 downgrade request (cache hit on 2nd), got %d", hits)
	}
}

// grantCounter is a token endpoint that counts grants and mints a fresh token
// each time, for asserting whether a call hit the network or a cache.
func grantCounter(t *testing.T) (url string, hits func() int) {
	t.Helper()
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		mu.Unlock()
		io.WriteString(w, `{"access_token":"agent-tok","refresh_token":"rt1","expires_in":1800}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	return srv.URL, func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// A token minted by one invocation is persisted and reused by the next (a fresh
// process — the in-memory cache is empty), so only the first call grants.
func TestAccessTokenForAudiencePersistsAcrossProcesses(t *testing.T) {
	isolate(t)
	_, hits := grantCounter(t)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	// First invocation grants and persists the audience token.
	if tok, err := AccessTokenForAudience(context.Background(), "anvil-agent"); err != nil || tok != "agent-tok" {
		t.Fatalf("first call: tok=%q err=%v", tok, err)
	}
	c, _ := Load()
	if at, ok := c.AudienceTokens["anvil-agent"]; !ok || at.AccessToken != "agent-tok" {
		t.Fatalf("audience token not persisted: %+v", c.AudienceTokens)
	}

	// A "new process" — drop the in-memory cache — reuses the persisted token
	// with no second grant.
	resetAudCache()
	if tok, err := AccessTokenForAudience(context.Background(), "anvil-agent"); err != nil || tok != "agent-tok" {
		t.Fatalf("second call: tok=%q err=%v", tok, err)
	}
	if n := hits(); n != 1 {
		t.Errorf("expected 1 grant total (persisted hit on the 2nd), got %d", n)
	}
}

// An expired persisted token is ignored — the call re-grants and rewrites it.
func TestAccessTokenForAudienceExpiredPersistedRegrants(t *testing.T) {
	isolate(t)
	_, hits := grantCounter(t)
	if err := Save(Credentials{
		AccessToken:  "union-tok",
		RefreshToken: "rt0",
		Expiry:       time.Now().Add(time.Hour),
		AudienceTokens: map[string]AudienceToken{
			"anvil-agent": {AccessToken: "stale-tok", Expiry: time.Now().Add(-time.Minute)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	resetAudCache()
	tok, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	if err != nil || tok != "agent-tok" {
		t.Fatalf("expired persisted token should re-grant: tok=%q err=%v", tok, err)
	}
	if n := hits(); n != 1 {
		t.Errorf("expected a re-grant for the expired token, got %d grants", n)
	}
	c, _ := Load()
	if at := c.AudienceTokens["anvil-agent"]; at.AccessToken != "agent-tok" {
		t.Errorf("re-granted token not persisted: %q", at.AccessToken)
	}
}

func TestAccessTokenForAudienceNoRefreshFallback(t *testing.T) {
	isolate(t)
	if err := Save(Credentials{AccessToken: "union-tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	tok, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	if err != nil {
		t.Fatalf("AccessTokenForAudience: %v", err)
	}
	if tok != "union-tok" {
		t.Errorf("no-refresh fallback = %q, want the stored token", tok)
	}
}

func TestLoginAudiencesUnion(t *testing.T) {
	t.Setenv("DRIVERFORGE_AUTH_AUDIENCE", "")
	t.Setenv("DRIVERFORGE_AGENT_AUDIENCE", "")
	if got := loginAudiences(); got != "graph anvil-agent mqtt-broker" {
		t.Errorf("loginAudiences = %q, want \"graph anvil-agent\"", got)
	}
}

// An invalid_grant from the token endpoint means the session is gone — an
// anticipated condition that must render as a friendly card, never a crash.
func TestAccessTokenForAudienceInvalidGrantIsUserError(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant","error_description":"token inactive"}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	_, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a clierr.UserError", err)
	}
	if ue.Title != "Session expired" {
		t.Errorf("title = %q, want Session expired", ue.Title)
	}
}

// Any other token-endpoint failure (outage, 5xx, malformed body) is transient
// service trouble — also friendly, with a try-again hint.
func TestAccessTokenForAudienceServerErrorIsUserError(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"http 500", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"server_error"}`)
		}},
		{"malformed body", func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{not json`)
		}},
		{"empty access_token", func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"access_token":""}`)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolate(t)
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
			if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}

			_, err := AccessTokenForAudience(context.Background(), "anvil-agent")
			var ue *clierr.UserError
			if !errors.As(err, &ue) {
				t.Fatalf("error = %v, want a clierr.UserError", err)
			}
			if ue.Title != "Sign-in service unavailable" {
				t.Errorf("title = %q, want Sign-in service unavailable", ue.Title)
			}
		})
	}
}

// sealCredentialsDir makes the credentials directory unwritable, so the next
// Save fails while Load keeps working. Returns a restore func.
func sealCredentialsDir(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block writes")
	}
	cfg, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg, "driverforge")
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	// Guard the setup: if Save still succeeds the test proves nothing.
	if err := Save(Credentials{AccessToken: "probe"}); err == nil {
		t.Fatal("credentials dir is still writable; the failure path would not be exercised")
	}
}

// The grant rotated the refresh token but persisting it failed: the old refresh
// token is already spent server-side, so carrying on would leave the session
// bricked with no signal. The call must fail loudly — and friendly, since a
// broken keychain/disk is not a CLI bug.
func TestAccessTokenForAudienceRotatedSaveFailureIsUserError(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// rt1 != the stored rt0 — a rotation.
		io.WriteString(w, `{"access_token":"agent-tok","refresh_token":"rt1","expires_in":1800}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sealCredentialsDir(t)

	_, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	if err == nil {
		t.Fatal("a failed rotated-refresh-token save must not be swallowed")
	}
	var ue *clierr.UserError
	if !errors.As(err, &ue) {
		t.Fatalf("error = %v, want a clierr.UserError", err)
	}
	if ue.Title != "Sign-in service unavailable" {
		t.Errorf("title = %q, want Sign-in service unavailable", ue.Title)
	}
}

// The mirror case: the server returned the SAME refresh token, so nothing was
// spent and the only thing lost is the audience-token cache write. That costs a
// re-grant next run — it must not fail the call the user asked for.
func TestAccessTokenForAudienceUnrotatedSaveFailureIsTolerated(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Same refresh token as stored — no rotation.
		io.WriteString(w, `{"access_token":"agent-tok","refresh_token":"rt0","expires_in":1800}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	sealCredentialsDir(t)

	tok, err := AccessTokenForAudience(context.Background(), "anvil-agent")
	if err != nil {
		t.Fatalf("an un-rotated save failure is only a lost cache write: %v", err)
	}
	if tok != "agent-tok" {
		t.Errorf("token = %q, want agent-tok", tok)
	}
}

// AgentAccessToken is the agent-audience wrapper; it must narrow to the agent
// audience rather than handing back the union token.
func TestAgentAccessTokenUsesAgentAudience(t *testing.T) {
	isolate(t)
	t.Setenv("DRIVERFORGE_AGENT_AUDIENCE", "")
	var gotAud string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		gotAud = r.Form.Get("audience")
		io.WriteString(w, `{"access_token":"agent-tok","refresh_token":"rt0","expires_in":1800}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}

	tok, err := AgentAccessToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != "agent-tok" {
		t.Errorf("token = %q, want agent-tok", tok)
	}
	if gotAud != resolveAgentAudience() {
		t.Errorf("audience = %q, want %q", gotAud, resolveAgentAudience())
	}
	if gotAud == "" {
		t.Error("AgentAccessToken must narrow to an audience, not request the union")
	}
}

// Only a dead grant is ErrSessionExpired; a transport failure is the transient
// "sign-in unavailable" condition, never a prompt to sign in again.
func TestAccessTokenForAudienceExpiredVersusUnreachable(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"invalid_grant"}`)
	}))
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)
	if err := Save(Credentials{AccessToken: "union-tok", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessTokenForAudience(context.Background(), "mqtt-broker"); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("invalid_grant: error = %v, want ErrSessionExpired", err)
	}

	srv.Close() // connection refused from here on
	_, err := AccessTokenForAudience(context.Background(), "mqtt-broker")
	var ue *clierr.UserError
	if !errors.As(err, &ue) || errors.Is(err, ErrSessionExpired) {
		t.Fatalf("transport failure: error = %v, want a non-expired UserError", err)
	}
	if ue.Title != "Sign-in service unavailable" {
		t.Errorf("title = %q, want Sign-in service unavailable", ue.Title)
	}
}
