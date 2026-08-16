package cli

import (
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

// newLoginCmd authenticates the user via the OAuth2 device authorization grant
// (RFC 8628): the user authorizes in their own browser and the CLI polls for a
// token. The CLI never handles the user's credentials.
func newLoginCmd() *cobra.Command {
	var noBrowser bool
	var orgFlag string
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate with Driverforge",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()

			// The refresh token is a long-lived bearer credential; an http:// auth
			// endpoint (other than localhost) POSTs it in the clear. Warn but don't
			// block — a deliberate lab/proxy setup is a legitimate override.
			if auth.InsecureAuthURL() {
				ui.Fprintln(out, ui.Warning("DRIVERFORGE_AUTH_URL uses http:// — your session token will be sent unencrypted. Use https outside local development."))
			}

			da, err := auth.RequestDeviceCode(ctx)
			if err != nil {
				// Reaching the sign-in service at all is an operational
				// precondition (offline, VPN, outage) — not a CLI bug.
				return clierr.WrapT(err, "Sign-in unavailable", "couldn't reach the sign-in service", "check your connection and try again")
			}

			fmt.Fprintln(out)
			ui.Fprintln(out, ui.DeviceLoginCard(out, da.VerificationURI, da.UserCode))
			fmt.Fprintln(out)

			openURL := da.VerificationURIComplete
			if openURL == "" {
				openURL = da.VerificationURI
			}
			if !noBrowser && ui.Interactive(flagNoTUI) {
				if err := auth.OpenBrowser(openURL); err == nil {
					ui.Fprintln(out, ui.Muted("Opened your browser…"))
				}
			}

			// Poll for authorization, then persist the token, behind a single
			// spinner whose line is replaced in place with the "Logged in as …"
			// confirmation on success.
			err = ui.WaitFor(ctx, out, ui.Interactive(flagNoTUI),
				"Waiting for authorization…",
				func() (string, error) {
					tok, e := auth.PollToken(ctx, da)
					if e != nil {
						return "", auth.PollError(e)
					}
					creds, e := auth.Persist(tok)
					if e != nil {
						return "", fmt.Errorf("saving credentials: %w", e)
					}
					// Warm the agent-audience token now (it persists across
					// invocations), so the first deploy/sync after login skips the
					// ~800ms grant too. Best-effort: login succeeds regardless.
					_, _ = auth.AgentAccessToken(ctx)
					return "Logged in as " + displayUser(creds), nil
				})
			if err != nil {
				return err
			}
			fmt.Fprintln(out) // breathing room before the org picker / confirmation

			// Select the organization to work in. Expected conditions (a bad --org
			// value, a cancelled picker) are friendly and never fail login; an
			// unexpected error (e.g. the API is unreachable or returns 5xx/404) is a
			// real bug — let it propagate so the top-level handler renders a crash
			// card and offers to report it.
			if err := selectOrgAfterLogin(ctx, out, orgFlag); err != nil {
				if clierr.IsUser(err) {
					ui.RenderUserError(out, err)
					return nil
				}
				return err
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "do not open a browser automatically")
	cmd.Flags().StringVar(&orgFlag, "org", "", "organization to use (name, slug, or id); skips the picker")
	return cmd
}

// newLogoutCmd revokes the refresh token (best-effort) and removes the stored
// credentials. It is idempotent.
func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Log out and remove stored credentials",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			c, err := auth.Load()
			if err != nil {
				if errors.Is(err, auth.ErrNotLoggedIn) {
					// No credentials, but a stale org/device context may linger
					// (e.g. logged out before this cleared it) — wipe it so the
					// prompt section doesn't keep showing a dead org.
					_ = orgctx.Clear()
					ui.Fprintln(out, ui.Muted("Already logged out."))
					return nil
				}
				return err
			}
			// Best-effort server-side revocation: local logout proceeds regardless,
			// but if the server refused we say so — the token stays valid until it
			// expires, which the user may want to act on.
			if err := auth.Revoke(cmd.Context(), c.RefreshToken); err != nil {
				ui.Fprintln(out, ui.Muted("Couldn't revoke the session server-side ("+err.Error()+"); removing local credentials anyway."))
			}
			if err := auth.Delete(); err != nil {
				return fmt.Errorf("removing credentials: %w", err)
			}
			if err := orgctx.Clear(); err != nil {
				return fmt.Errorf("clearing organization context: %w", err)
			}
			ui.Fprintln(out, ui.Success("Logged out."))
			return nil
		},
	}
}

// newWhoamiCmd shows the currently logged-in user, refreshing the token if it
// has expired.
func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the currently logged-in user",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := auth.Current(cmd.Context())
			if err != nil {
				return err // ErrNotLoggedIn carries a "run `driverforge login`" hint
			}
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, displayUser(c))
			if !c.Expiry.IsZero() {
				fmt.Fprintf(out, "token expires %s\n", c.Expiry.Local().Format(time.RFC1123))
			}
			return nil
		},
	}
}

// displayUser picks the best human label for a credential bundle: the full name
// (with email in parentheses when both are present), else email, else the
// subject id.
func displayUser(c auth.Credentials) string {
	switch {
	case c.Name != "" && c.Email != "":
		return fmt.Sprintf("%s (%s)", c.Name, c.Email)
	case c.Name != "":
		return c.Name
	case c.Email != "":
		return c.Email
	case c.Subject != "":
		return c.Subject
	default:
		return "(unknown user)"
	}
}
