package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// fakeJWT builds an unsigned JWT carrying the given claims (display-only; the
// CLI never verifies the signature).
func fakeJWT(claims map[string]string) string {
	payload, _ := json.Marshal(claims)
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// useFileBackend points credential storage at a temp dir and forces the file
// backend, so tests are deterministic and never touch the real keychain.
func useFileBackend(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("HOME", dir)            // darwin: ~/Library/Application Support
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	return dir
}

func TestStoreRoundTripFileBackend(t *testing.T) {
	useFileBackend(t)

	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("fresh Load = %v, want ErrNotLoggedIn", err)
	}

	in := Credentials{
		AccessToken:  "atk",
		RefreshToken: "rtk",
		Expiry:       time.Now().Add(30 * time.Minute).Round(time.Second),
		Email:        "a@b.com",
		Subject:      "u-1",
		Issuer:       authHost(),
	}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.AccessToken != in.AccessToken || got.RefreshToken != in.RefreshToken || got.Email != in.Email {
		t.Errorf("round-trip mismatch: %+v vs %+v", got, in)
	}

	// File must be 0600.
	path, _ := credentialsPath(authHost())
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credentials: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode = %o, want 600", perm)
	}

	if err := Delete(); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("after Delete, Load = %v, want ErrNotLoggedIn", err)
	}
	// Delete is idempotent.
	if err := Delete(); err != nil {
		t.Errorf("second Delete should be a no-op, got %v", err)
	}
}

func TestDeleteIdempotentOnEmptyStore(t *testing.T) {
	useFileBackend(t)
	if err := Delete(); err != nil {
		t.Errorf("Delete on empty store = %v, want nil", err)
	}
}

func TestClaimsFromJWT(t *testing.T) {
	payload, _ := json.Marshal(map[string]string{"sub": "user-123", "email": "user@example.com", "name": "Ada Lovelace"})
	token := "header." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"

	sub, email, name := claimsFromJWT(token)
	if sub != "user-123" || email != "user@example.com" || name != "Ada Lovelace" {
		t.Errorf("claimsFromJWT = (%q, %q, %q), want (user-123, user@example.com, Ada Lovelace)", sub, email, name)
	}

	// Junk input must not panic and yields empties.
	if s, e, n := claimsFromJWT("not-a-jwt"); s != "" || e != "" || n != "" {
		t.Errorf("junk token = (%q, %q, %q), want empty", s, e, n)
	}
	if s, e, n := claimsFromJWT(""); s != "" || e != "" || n != "" {
		t.Errorf("empty token = (%q, %q, %q), want empty", s, e, n)
	}
}

func TestCredentialsFromTokenPrefersIDToken(t *testing.T) {
	// The access token carries only sub; email/name live in the id_token.
	access := fakeJWT(map[string]string{"sub": "user-123"})
	id := fakeJWT(map[string]string{"sub": "user-123", "email": "ada@example.com", "name": "Ada Lovelace"})
	tok := (&oauth2.Token{AccessToken: access}).WithExtra(map[string]any{"id_token": id})

	c := credentialsFromToken(tok)
	if c.Subject != "user-123" || c.Email != "ada@example.com" || c.Name != "Ada Lovelace" {
		t.Errorf("credentialsFromToken = (sub=%q email=%q name=%q), want user-123/ada@example.com/Ada Lovelace", c.Subject, c.Email, c.Name)
	}

	// With no id_token (e.g. a refresh grant), the subject still falls back to
	// the access token; email/name are left empty for the caller to preserve.
	bare := credentialsFromToken(&oauth2.Token{AccessToken: access})
	if bare.Subject != "user-123" || bare.Email != "" || bare.Name != "" {
		t.Errorf("credentialsFromToken(no id_token) = (sub=%q email=%q name=%q), want user-123 with empty email/name", bare.Subject, bare.Email, bare.Name)
	}
}

func TestEndpointOverrides(t *testing.T) {
	t.Setenv("DRIVERFORGE_AUTH_URL", "http://localhost:4444/")
	t.Setenv("DRIVERFORGE_AUTH_CLIENT_ID", "test-client")
	t.Setenv("DRIVERFORGE_AUTH_AUDIENCE", "test-aud")

	if got, want := deviceAuthURL(), "http://localhost:4444/oauth2/device/auth"; got != want {
		t.Errorf("deviceAuthURL = %q, want %q", got, want)
	}
	if got, want := tokenURL(), "http://localhost:4444/oauth2/token"; got != want {
		t.Errorf("tokenURL = %q, want %q", got, want)
	}
	if got, want := authHost(), "localhost:4444"; got != want {
		t.Errorf("authHost = %q, want %q", got, want)
	}
	if got := resolveClientID(); got != "test-client" {
		t.Errorf("resolveClientID = %q, want test-client", got)
	}
	if got := resolveAudience(); got != "test-aud" {
		t.Errorf("resolveAudience = %q, want test-aud", got)
	}
	cfg := oauthConfig()
	if cfg.ClientID != "test-client" || cfg.Endpoint.TokenURL != "http://localhost:4444/oauth2/token" {
		t.Errorf("oauthConfig not wired from env: %+v", cfg)
	}
}

func TestLoginScopesIncludeGraph(t *testing.T) {
	// Login must request the graph scope (the Graph API fails closed without it),
	// on top of the OIDC base scopes.
	want := map[string]bool{"openid": true, "profile": true, "email": true, "offline_access": true, "graph": true}
	got := map[string]bool{}
	for _, s := range loginScopes() {
		got[s] = true
	}
	for s := range want {
		if !got[s] {
			t.Errorf("loginScopes missing %q; got %v", s, loginScopes())
		}
	}
	if oauthConfig().Scopes == nil || len(oauthConfig().Scopes) != len(want) {
		t.Errorf("oauthConfig scopes = %v, want %d scopes incl. graph", oauthConfig().Scopes, len(want))
	}

	// The agent (downgrade) scopes must NOT carry the graph scope.
	for _, s := range baseScopes() {
		if s == "graph" {
			t.Errorf("baseScopes must not include the graph scope: %v", baseScopes())
		}
	}

	// The graph scope name is overridable (so a renamed seed needs no rebuild).
	t.Setenv("DRIVERFORGE_AUTH_GRAPH_SCOPE", "graph:rw")
	if resolveGraphScope() != "graph:rw" {
		t.Errorf("resolveGraphScope override = %q, want graph:rw", resolveGraphScope())
	}
	if got := loginScopes(); got[len(got)-1] != "graph:rw" {
		t.Errorf("loginScopes did not honour override: %v", got)
	}
}

func TestCredentialsPathUnderConfigDir(t *testing.T) {
	dir := useFileBackend(t)
	path, err := credentialsPath("auth.example.test:4444")
	if err != nil {
		t.Fatal(err)
	}
	// Keyed by the (sanitised) auth host so environments coexist in file mode.
	if got := filepath.Base(path); got != "credentials-auth.example.test_4444.json" {
		t.Errorf("credentials filename = %q, want credentials-auth.example.test_4444.json", got)
	}
	// Sanity: path is under the temp HOME we set.
	if rel, err := filepath.Rel(dir, path); err != nil || rel == "" {
		t.Errorf("credentials path %q not under temp HOME %q", path, dir)
	}
}

// Environments must coexist in file mode exactly as they do in the keychain:
// switching DRIVERFORGE_AUTH_URL never returns, overwrites, or deletes another
// environment's tokens.
func TestFileBackendPerHostIsolation(t *testing.T) {
	useFileBackend(t)

	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.prod.test")
	if err := Save(Credentials{AccessToken: "prod-tok"}); err != nil {
		t.Fatal(err)
	}

	// The dev environment sees no credentials, and logging in there must not
	// touch prod's.
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.dev.test")
	if _, err := Load(); !errors.Is(err, ErrNotLoggedIn) {
		t.Fatalf("dev Load = %v, want ErrNotLoggedIn (prod tokens leaked?)", err)
	}
	if err := Save(Credentials{AccessToken: "dev-tok"}); err != nil {
		t.Fatal(err)
	}
	if err := Delete(); err != nil { // dev logout
		t.Fatal(err)
	}

	// Prod credentials survived the dev login + logout.
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.prod.test")
	got, err := Load()
	if err != nil {
		t.Fatalf("prod Load after dev logout: %v", err)
	}
	if got.AccessToken != "prod-tok" {
		t.Errorf("prod token = %q, want prod-tok", got.AccessToken)
	}
}

// Rewriting credentials (token refresh) must restore 0600 even if the file was
// somehow left with looser permissions — the temp+rename write guarantees it.
func TestFileBackendRewriteRestores0600(t *testing.T) {
	useFileBackend(t)
	if err := Save(Credentials{AccessToken: "a"}); err != nil {
		t.Fatal(err)
	}
	path, _ := credentialsPath(authHost())
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(Credentials{AccessToken: "b"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials file mode after rewrite = %o, want 600", perm)
	}
}
