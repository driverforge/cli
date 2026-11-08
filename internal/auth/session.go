package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/driverforge/cli/internal/clierr"
)

// Persist decodes the identity from a freshly minted token and saves the
// credential bundle. Returned for the caller to display ("Logged in as ...").
func Persist(tok *oauth2.Token) (Credentials, error) {
	c := credentialsFromToken(tok)
	if err := Save(c); err != nil {
		return Credentials{}, err
	}
	clearAudienceTokens() // minted under the previous sign-in
	return c, nil
}

// refreshWindow is how far before expiry Current() treats the access token as
// needing a refresh (a little wider than oauth2's own ~10s delta, so the
// cross-process lock is held while the actual refresh happens).
const refreshWindow = 30 * time.Second

// needsRefresh reports whether c's access token is expired or close enough that
// a refresh is due. A zero expiry is unknown, so we assume a refresh is needed.
func (c Credentials) needsRefresh() bool {
	if c.Expiry.IsZero() {
		return true
	}
	return time.Now().After(c.Expiry.Add(-refreshWindow))
}

// Current returns the stored credentials, transparently refreshing the access
// token (and persisting the rotated tokens) when it has expired. It returns
// ErrNotLoggedIn when no credentials are stored.
func Current(ctx context.Context) (Credentials, error) {
	c, err := Load()
	if err != nil {
		return Credentials{}, err
	}
	if c.RefreshToken == "" || !c.needsRefresh() {
		return c, nil // nothing to refresh with, or still valid — fast path, no lock
	}

	// Serialise the refresh across processes: a concurrent invocation must not
	// consume the same rotating refresh token. Re-load under the lock — the
	// process we waited on may have just refreshed it for us.
	release, _ := lockCredentials()
	defer release()
	if fresh, err := Load(); err == nil {
		c = fresh
		if c.RefreshToken == "" || !c.needsRefresh() {
			return c, nil
		}
	}

	cfg := oauthConfig()
	src := cfg.TokenSource(ctx, &oauth2.Token{
		AccessToken:  c.AccessToken,
		RefreshToken: c.RefreshToken,
		Expiry:       c.Expiry,
		TokenType:    "Bearer",
	})
	newTok, err := src.Token() // refreshes only if the access token is expired
	if err != nil {
		return Credentials{}, refreshError(err)
	}
	if newTok.AccessToken == c.AccessToken {
		return c, nil // still valid, unchanged
	}

	updated := credentialsFromToken(newTok)
	// A refresh grant often omits the id_token, so identity claims can come back
	// empty — preserve what we already decoded at login.
	if updated.Email == "" {
		updated.Email = c.Email
	}
	if updated.Name == "" {
		updated.Name = c.Name
	}
	if updated.Subject == "" {
		updated.Subject = c.Subject
	}
	// Persisting the rotated tokens is NOT best-effort: the old refresh token is
	// already spent server-side, so a failed save silently bricks the next run.
	if err := Save(updated); err != nil {
		return Credentials{}, refreshError(fmt.Errorf("saving refreshed credentials: %w", err))
	}
	return updated, nil
}

// refreshError turns a token-refresh failure into a friendly clierr.UserError so
// it renders as a card and is never reported to Sentry — a refresh failure is an
// operational condition (expired/revoked grant, or the sign-in service / its token
// hook being down), not a CLI bug. invalid_grant means the session is gone and the
// user must sign in again; anything else (server_error, temporary outage, transport
// failure) is transient — try again, and re-login only if it persists.
func refreshError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) && re.ErrorCode == "invalid_grant" {
		return sessionExpiredError(err)
	}
	return signInUnavailableError(err)
}

// ErrSessionExpired marks a refresh that failed because the stored grant is
// dead (expired or revoked), as opposed to the sign-in service being unreachable
// or erroring. Match it with errors.Is to choose between "sign in again" and
// "try again".
var ErrSessionExpired = errors.New("session expired")

// sessionExpiredError is the friendly condition for a dead grant (invalid_grant):
// the session is gone and the user must sign in again.
func sessionExpiredError(cause error) *clierr.UserError {
	cause = fmt.Errorf("%w: %w", ErrSessionExpired, cause)
	return &clierr.UserError{
		Code:    clierr.CodeAuthRequired,
		Title:   "Session expired",
		Message: "your sign-in has expired or been revoked",
		Hint:    "run `driverforge login` to sign in again",
		Err:     cause,
	}
}

// signInUnavailableError is the friendly condition for any other token-endpoint
// failure (server_error, outage, malformed response) — transient, try again.
func signInUnavailableError(cause error) *clierr.UserError {
	return &clierr.UserError{
		Code:    clierr.CodeServiceUnavailable,
		Title:   "Sign-in service unavailable",
		Message: "couldn't refresh your session — the sign-in service is having trouble",
		Hint:    "try again in a moment; if it persists, run `driverforge login`",
		Err:     cause,
	}
}

// credentialsFromToken builds a Credentials bundle from an oauth2.Token. The
// display identity (email/name) comes from the OIDC id_token — the access token
// reliably carries only sub — so we read the id_token when present and fall back
// to the access token for the subject.
func credentialsFromToken(tok *oauth2.Token) Credentials {
	c := Credentials{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		Expiry:       tok.Expiry,
		Issuer:       authHost(),
	}
	c.Subject, c.Email, c.Name = claimsFromJWT(idToken(tok))
	if c.Subject == "" {
		c.Subject, _, _ = claimsFromJWT(tok.AccessToken)
	}
	return c
}

// idToken extracts the OIDC id_token from a token response, or "" when absent
// (e.g. a refresh grant that doesn't re-issue one).
func idToken(tok *oauth2.Token) string {
	if v, ok := tok.Extra("id_token").(string); ok {
		return v
	}
	return ""
}

// claimsFromJWT decodes the sub/email/name claims from a JWT's payload for
// display only. It does NOT verify the signature (the server does that); junk
// input yields empty strings.
func claimsFromJWT(token string) (sub, email, name string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", "", ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if payload, err = base64.URLEncoding.DecodeString(parts[1]); err != nil {
			return "", "", ""
		}
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	_ = json.Unmarshal(payload, &claims)
	return claims.Sub, claims.Email, claims.Name
}
