package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/driverforge/cli/internal/clierr"
)

// RequestDeviceCode starts the device authorization flow and returns the user
// code and verification URL for the user to visit. The token is obtained
// afterwards via PollToken.
func RequestDeviceCode(ctx context.Context) (*oauth2.DeviceAuthResponse, error) {
	cfg := oauthConfig()
	return cfg.DeviceAuth(ctx, oauth2.SetAuthURLParam("audience", loginAudiences()))
}

// PollToken polls the token endpoint until the user authorizes the device (or
// the code expires / is denied). x/oauth2 honors the response's poll interval
// and the context deadline.
func PollToken(ctx context.Context, da *oauth2.DeviceAuthResponse) (*oauth2.Token, error) {
	cfg := oauthConfig()
	return cfg.DeviceAccessToken(ctx, da, oauth2.SetAuthURLParam("audience", loginAudiences()))
}

// PollError classifies a PollToken failure. The RFC 8628 terminal outcomes —
// the user denying the request or letting the device code lapse — are routine,
// anticipated conditions and become friendly UserErrors; anything else is
// wrapped as a plain login failure and stays reportable.
func PollError(err error) error {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		switch re.ErrorCode {
		case "access_denied":
			return &clierr.UserError{
				Title:   "Login declined",
				Message: "the sign-in request was declined in the browser",
				Hint:    "run `driverforge login` to try again",
				Err:     err,
			}
		case "expired_token":
			return &clierr.UserError{
				Title:   "Login code expired",
				Message: "the sign-in code expired before it was approved",
				Hint:    "run `driverforge login` to get a fresh code",
				Err:     err,
			}
		}
	}
	return fmt.Errorf("login failed: %w", err)
}

// Revoke best-effort revokes a refresh token at Hydra's revocation endpoint so
// logout invalidates the session server-side too. As a public client we send
// client_id in the body and no secret. Errors are returned but callers treat
// revocation as best-effort.
func Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	form := url.Values{}
	form.Set("token", token)
	form.Set("token_type_hint", "refresh_token")
	form.Set("client_id", resolveClientID())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// RFC 7009: a successful revocation returns 200. Hydra also treats an unknown
	// token as success, so a non-2xx here means the server genuinely refused
	// (bad client_id, endpoint moved) — surface it so the best-effort caller can
	// note that the server-side session may still be live, rather than silently
	// no-op'ing on a 4xx.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("revoke endpoint returned %s", resp.Status)
	}
	return nil
}
