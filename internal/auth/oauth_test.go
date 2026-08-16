package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/oauth2"

	"github.com/driverforge/cli/internal/clierr"
)

// The RFC 8628 terminal outcomes — denied in the browser, or an expired device
// code — are routine and must map to friendly UserErrors; anything else stays
// a reportable login failure.
func TestPollError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantUser  bool
		wantTitle string
	}{
		{"denied", &oauth2.RetrieveError{ErrorCode: "access_denied"}, true, "Login declined"},
		{"expired", &oauth2.RetrieveError{ErrorCode: "expired_token"}, true, "Login code expired"},
		{"wrapped denied", fmt.Errorf("poll: %w", &oauth2.RetrieveError{ErrorCode: "access_denied"}), true, "Login declined"},
		{"server error", &oauth2.RetrieveError{ErrorCode: "server_error"}, false, ""},
		{"transport error", errors.New("connection refused"), false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PollError(tc.err)
			var ue *clierr.UserError
			isUser := errors.As(got, &ue)
			if isUser != tc.wantUser {
				t.Fatalf("IsUser = %v, want %v (err=%v)", isUser, tc.wantUser, got)
			}
			if tc.wantUser && ue.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", ue.Title, tc.wantTitle)
			}
			if !tc.wantUser && !errors.Is(got, tc.err) {
				t.Errorf("non-user error must wrap the original: %v", got)
			}
		})
	}
}

// Revoke must treat a non-2xx from the revocation endpoint as a failure so the
// best-effort caller can note that the server-side session may still be live; a
// 2xx (including Hydra's "unknown token" success) is a clean revocation.
func TestRevoke(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"ok", http.StatusOK, false},
		{"no content", http.StatusNoContent, false},
		{"unauthorized", http.StatusUnauthorized, true},
		{"server error", http.StatusInternalServerError, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)

			err := Revoke(context.Background(), "some-refresh-token")
			if (err != nil) != tc.wantErr {
				t.Fatalf("Revoke err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

// An empty token is a no-op — logout with no refresh token must not dial out.
func TestRevokeEmptyTokenNoRequest(t *testing.T) {
	t.Setenv("DRIVERFORGE_AUTH_URL", "http://127.0.0.1:1") // would fail if dialled
	if err := Revoke(context.Background(), ""); err != nil {
		t.Fatalf("empty-token Revoke should be a no-op, got %v", err)
	}
}

// InsecureAuthURL flags cleartext http to a non-local host, but not https and
// not the sanctioned http://localhost dev setup.
func TestInsecureAuthURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://auth.driverforge.com", false},
		{"http://localhost:4444", false},
		{"http://127.0.0.1:4444", false},
		{"http://auth.driverforge.test", false},
		{"http://dev.localhost:4444", false},
		{"http://auth.example.com", true},
		{"http://10.0.0.5:4444", true},
	}
	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			t.Setenv("DRIVERFORGE_AUTH_URL", tc.url)
			if got := InsecureAuthURL(); got != tc.want {
				t.Errorf("InsecureAuthURL(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}
