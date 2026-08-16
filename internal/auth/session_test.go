package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/driverforge/cli/internal/clierr"
)

func TestRefreshError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantHint string // substring expected in the hint
	}{
		{
			name:     "expired/revoked grant points at re-login",
			err:      &oauth2.RetrieveError{ErrorCode: "invalid_grant"},
			wantHint: "driverforge login",
		},
		{
			name:     "backend token-hook failure is transient",
			err:      &oauth2.RetrieveError{ErrorCode: "server_error"},
			wantHint: "try again",
		},
		{
			name:     "transport failure is transient",
			err:      fmt.Errorf("dial tcp: connection refused"),
			wantHint: "try again",
		},
		{
			name:     "wrapped invalid_grant still detected",
			err:      fmt.Errorf("refresh: %w", &oauth2.RetrieveError{ErrorCode: "invalid_grant"}),
			wantHint: "driverforge login",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := refreshError(c.err)
			// Always a UserError → friendly card, never reported to Sentry.
			if !clierr.IsUser(got) {
				t.Fatalf("refreshError should yield a clierr.UserError, got %T", got)
			}
			var ue *clierr.UserError
			errors.As(got, &ue)
			if !strings.Contains(ue.Hint, c.wantHint) {
				t.Errorf("hint = %q, want it to contain %q", ue.Hint, c.wantHint)
			}
			// The original cause is preserved for errors.Is/As.
			if !errors.Is(got, c.err) {
				t.Errorf("refreshError should wrap the original cause")
			}
		})
	}
}

func TestNeedsRefresh(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero expiry (unknown) → refresh", time.Time{}, true},
		{"expired → refresh", now.Add(-time.Minute), true},
		{"within window → refresh", now.Add(10 * time.Second), true},
		{"fresh → no refresh", now.Add(time.Hour), false},
	}
	for _, tc := range cases {
		if got := (Credentials{Expiry: tc.expiry}).needsRefresh(); got != tc.want {
			t.Errorf("%s: needsRefresh = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A fresh access token is returned without any network call (fast path, no lock).
func TestCurrentFastPathNoRefresh(t *testing.T) {
	isolate(t)
	t.Setenv("DRIVERFORGE_AUTH_URL", "http://127.0.0.1:1") // would fail if dialled
	if err := Save(Credentials{AccessToken: "still-good", RefreshToken: "rt0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	c, err := Current(context.Background())
	if err != nil {
		t.Fatalf("fresh token should not refresh: %v", err)
	}
	if c.AccessToken != "still-good" {
		t.Errorf("access token = %q, want still-good", c.AccessToken)
	}
}

// An expired token refreshes against the token endpoint, persists the rotated
// tokens, and preserves identity claims the refresh grant omits.
func TestCurrentRefreshesAndPersists(t *testing.T) {
	isolate(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"new-at","refresh_token":"rt1","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)

	if err := Save(Credentials{
		AccessToken: "old-at", RefreshToken: "rt0", Expiry: time.Now().Add(-time.Minute),
		Email: "dev@example.com", Name: "Dev", Subject: "sub-1",
	}); err != nil {
		t.Fatal(err)
	}

	c, err := Current(context.Background())
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if c.AccessToken != "new-at" {
		t.Errorf("access token = %q, want new-at", c.AccessToken)
	}
	// Identity claims (absent from the refresh grant) preserved.
	if c.Email != "dev@example.com" || c.Name != "Dev" || c.Subject != "sub-1" {
		t.Errorf("identity claims not preserved: %+v", c)
	}
	// Rotated tokens persisted for the next run.
	stored, _ := Load()
	if stored.AccessToken != "new-at" || stored.RefreshToken != "rt1" {
		t.Errorf("rotated tokens not persisted: %+v", stored)
	}
}

// Concurrent Current() calls needing a refresh must serialise: the lock lets
// the first refresh, and the rest pick up its rotated token — so the token
// endpoint is hit exactly once, not once per caller (which would race the
// rotating refresh token and trip Hydra's reuse detection).
func TestCurrentSerialisesConcurrentRefresh(t *testing.T) {
	isolate(t)
	var mu sync.Mutex
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		hits++
		mu.Unlock()
		time.Sleep(20 * time.Millisecond) // widen the race window
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"new-at","refresh_token":"rt1","token_type":"Bearer","expires_in":3600}`)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_AUTH_URL", srv.URL)

	if err := Save(Credentials{AccessToken: "old", RefreshToken: "rt0", Expiry: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Current(context.Background()); err != nil {
				t.Errorf("Current: %v", err)
			}
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (concurrent refreshes must serialise)", hits)
	}
}
