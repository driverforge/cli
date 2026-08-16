package updatecheck

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// manifestServing stands up a manifest endpoint returning version, counts the
// requests, and points Notify at it.
func manifestServing(t *testing.T, version string) func() int {
	t.Helper()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.Write([]byte(`{"version":"` + version + `"}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_UPDATE_MANIFEST_URL", srv.URL)
	return func() int { return hits }
}

// clearNotifyOptOuts unsets every variable that suppresses the notifier, so a
// test starts from "would notify" — CI in particular is set in our own CI.
func clearNotifyOptOuts(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DRIVERFORGE_NO_UPDATE_CHECK", "NO_UPDATE_NOTIFIER", "CI"} {
		t.Setenv(k, "")
	}
}

// The happy path: a newer release is available, so the notice is printed with
// both versions and an upgrade command.
func TestNotifyPrintsNoticeWhenNewer(t *testing.T) {
	clearNotifyOptOuts(t)
	isolateCache(t)
	manifestServing(t, "1.5.0")

	var buf bytes.Buffer
	Notify(context.Background(), "1.4.0", &buf)

	out := buf.String()
	if !strings.Contains(out, "1.5.0") || !strings.Contains(out, "1.4.0") {
		t.Errorf("notice should name both versions, got %q", out)
	}
	if !strings.Contains(out, UpgradeHint()) {
		t.Errorf("notice should carry the upgrade hint %q, got %q", UpgradeHint(), out)
	}
}

// Each opt-out must suppress the notice on its own — and must do so before any
// network request, so an opted-out user never pays the manifest fetch.
func TestNotifyRespectsOptOuts(t *testing.T) {
	for _, envVar := range []string{"DRIVERFORGE_NO_UPDATE_CHECK", "NO_UPDATE_NOTIFIER", "CI"} {
		t.Run(envVar, func(t *testing.T) {
			clearNotifyOptOuts(t)
			isolateCache(t)
			hits := manifestServing(t, "1.5.0")
			t.Setenv(envVar, "1")

			var buf bytes.Buffer
			Notify(context.Background(), "1.4.0", &buf)

			if buf.Len() != 0 {
				t.Errorf("%s set but notice printed: %q", envVar, buf.String())
			}
			if hits() != 0 {
				t.Errorf("%s set but the manifest was still fetched (%d hits)", envVar, hits())
			}
		})
	}
}

// A non-release build ("dev", a bare commit, an empty version) has nothing
// meaningful to compare against, so it must stay silent and skip the network.
func TestNotifySilentForNonReleaseBuild(t *testing.T) {
	for _, current := range []string{"dev", "", "1.4", "abc1234"} {
		t.Run("current="+current, func(t *testing.T) {
			clearNotifyOptOuts(t)
			isolateCache(t)
			hits := manifestServing(t, "1.5.0")

			var buf bytes.Buffer
			Notify(context.Background(), current, &buf)

			if buf.Len() != 0 {
				t.Errorf("non-release build %q printed a notice: %q", current, buf.String())
			}
			if hits() != 0 {
				t.Errorf("non-release build %q still fetched the manifest", current)
			}
		})
	}
}

// Writing to a pipe (or any redirected file) means the output is being consumed
// by something other than a human — the notice would corrupt it.
func TestNotifySilentWhenNotATerminal(t *testing.T) {
	clearNotifyOptOuts(t)
	isolateCache(t)
	manifestServing(t, "1.5.0")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	Notify(context.Background(), "1.4.0", w)
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("notice written to a non-terminal file: %q", buf.String())
	}
}

// Same version, and an older "latest" than the running build (a rolled-back
// manifest, or a user on a pre-release ahead of stable): neither is an upgrade.
func TestNotifySilentWhenNotNewer(t *testing.T) {
	cases := []struct{ name, latest, current string }{
		{"same version", "1.4.0", "1.4.0"},
		{"latest is older", "1.3.0", "1.4.0"},
		{"pre-release of the same version", "1.4.0", "1.4.0-rc1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clearNotifyOptOuts(t)
			isolateCache(t)
			manifestServing(t, tc.latest)

			var buf bytes.Buffer
			Notify(context.Background(), tc.current, &buf)

			if buf.Len() != 0 {
				t.Errorf("latest=%s current=%s printed a notice: %q", tc.latest, tc.current, buf.String())
			}
		})
	}
}

// An unreachable or broken manifest must never disrupt the command: no notice,
// no error, no panic.
func TestNotifySwallowsFetchFailure(t *testing.T) {
	clearNotifyOptOuts(t)
	isolateCache(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("DRIVERFORGE_UPDATE_MANIFEST_URL", srv.URL)

	var buf bytes.Buffer
	Notify(context.Background(), "1.4.0", &buf)

	if buf.Len() != 0 {
		t.Errorf("a failed fetch printed something: %q", buf.String())
	}
}

// Within the TTL the notice comes from the cache, so a second command in the
// same day notifies without a second network request.
func TestNotifyUsesCacheWithinTTL(t *testing.T) {
	clearNotifyOptOuts(t)
	isolateCache(t)
	hits := manifestServing(t, "1.5.0")

	var first, second bytes.Buffer
	Notify(context.Background(), "1.4.0", &first)
	Notify(context.Background(), "1.4.0", &second)

	if first.Len() == 0 || second.Len() == 0 {
		t.Fatalf("both runs should notify (first=%q second=%q)", first.String(), second.String())
	}
	if hits() != 1 {
		t.Errorf("expected 1 manifest fetch across two runs, got %d", hits())
	}
}

// A cache entry older than the TTL is refreshed rather than trusted, so a
// release published since the last check is picked up.
func TestNotifyRefreshesStaleCache(t *testing.T) {
	clearNotifyOptOuts(t)
	isolateCache(t)
	writeCache(cacheData{LatestVersion: "1.4.1", CheckedAt: time.Now().Add(-checkInterval - time.Hour)})
	hits := manifestServing(t, "1.6.0")

	var buf bytes.Buffer
	Notify(context.Background(), "1.4.0", &buf)

	if hits() != 1 {
		t.Errorf("a stale cache must be refreshed, got %d fetches", hits())
	}
	if !strings.Contains(buf.String(), "1.6.0") {
		t.Errorf("notice should carry the refreshed version 1.6.0, got %q", buf.String())
	}
}
