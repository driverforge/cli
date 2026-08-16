package errreport

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/metrics"
	"github.com/driverforge/cli/internal/orgctx"
)

// resetSentryInit clears the package-global one-shot Sentry init so a test can
// (re)bind the SDK to its own fake ingest, independent of test order or -count
// (otherwise the first send in the process pins the DSN forever).
func resetSentryInit() {
	initOnce = sync.Once{}
	initOK = false
}

// eventually polls cond until it is true or the timeout elapses. Sentry delivery
// is asynchronous (Flush is best-effort), so a freshly sent event can land just
// after the call returns.
func eventually(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func clearOptOut(t *testing.T) {
	t.Helper()
	t.Setenv("DRIVERFORGE_NO_ERROR_REPORTING", "")
	t.Setenv("DRIVERFORGE_NO_METRICS", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("CI", "")
}

func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
}

func TestReportable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"not logged in", auth.ErrNotLoggedIn, false},
		{"wrapped not logged in", fmt.Errorf("x: %w", auth.ErrNotLoggedIn), false},
		{"no org", orgctx.ErrNoOrg, false},
		{"no device", orgctx.ErrNoDevice, false},
		{"user error", clierr.User("not a project", "run `driverforge init`"), false},
		{"wrapped user error", fmt.Errorf("deploy: %w", clierr.User("not a project", "")), false},
		// A raw error is no longer made non-reportable by a "run `driverforge …`" string —
		// anticipated conditions must be clierr.UserErrors (the only thing that counts).
		{"raw run-driverforge string is not special-cased", fmt.Errorf("boom (run `driverforge foo`)"), true},
		{"http 404", fmt.Errorf("graph api: HTTP 404: Not Found"), true},
		{"decode", fmt.Errorf("graph api: decoding data: unexpected EOF"), true},
	}
	for _, c := range cases {
		if got := Reportable(c.err); got != c.want {
			t.Errorf("%s: Reportable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestOptedOut(t *testing.T) {
	for _, v := range []string{"DRIVERFORGE_NO_ERROR_REPORTING", "DRIVERFORGE_NO_METRICS", "DO_NOT_TRACK", "CI"} {
		clearOptOut(t)
		t.Setenv(v, "1")
		if !optedOut() {
			t.Errorf("optedOut() = false with %s set", v)
		}
	}
	clearOptOut(t)
	if optedOut() {
		t.Error("optedOut() = true with no opt-out env")
	}
}

func TestEnabled(t *testing.T) {
	clearOptOut(t)
	t.Setenv("DRIVERFORGE_SENTRY_DSN", "https://k@example.test/1")
	if !enabled() {
		t.Error("enabled() = false with DSN + no opt-out")
	}
	t.Setenv("DO_NOT_TRACK", "1")
	if enabled() {
		t.Error("enabled() = true while opted out")
	}
}

func TestPersistConsent(t *testing.T) {
	isolateConfig(t)
	persist(metrics.ConsentGranted)
	if got := metrics.Load().ErrorReportingConsent; got != metrics.ConsentGranted {
		t.Errorf("persisted consent = %q, want granted", got)
	}
}

// TestHandleRespectsConsent exercises the send path against a local fake Sentry
// ingest (never the real DSN): denied → no request, granted → a request.
func TestHandleRespectsConsent(t *testing.T) {
	isolateConfig(t)
	clearOptOut(t)

	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	dsn := "http://0123456789abcdef0123456789abcdef@" + strings.TrimPrefix(srv.URL, "http://") + "/1"
	t.Setenv("DRIVERFORGE_SENTRY_DSN", dsn)
	resetSentryInit() // bind Sentry to this fake ingest, not a prior run's

	err := fmt.Errorf("graph api: HTTP 500: boom")

	persist(metrics.ConsentDenied)
	Handle(err, Invocation{Command: "org list"}, io.Discard)
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("denied consent should not send, got %d requests", n)
	}

	persist(metrics.ConsentGranted)
	Handle(err, Invocation{Command: "org list"}, io.Discard)
	if !eventually(2*time.Second, func() bool { return atomic.LoadInt32(&hits) > 0 }) {
		t.Fatal("granted consent should send a report")
	}
}
