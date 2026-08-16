package metrics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// isolateConfig points the config dir at a temp directory and clears opt-out
// env vars so tests are deterministic regardless of the host environment.
func isolateConfig(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)            // darwin: ~/Library/Application Support; linux fallback
	t.Setenv("XDG_CONFIG_HOME", dir) // linux
	t.Setenv("CI", "")
	t.Setenv("DO_NOT_TRACK", "")
	t.Setenv("DRIVERFORGE_NO_METRICS", "")
}

func TestConfigRoundTrip(t *testing.T) {
	isolateConfig(t)

	if got := Load(); got.MetricsConsent != ConsentUnset {
		t.Errorf("fresh Load consent = %q, want unset", got.MetricsConsent)
	}

	in := Config{AnonymousID: "abc123", MetricsConsent: ConsentGranted}
	if err := Save(in); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load()
	if got.AnonymousID != "abc123" || got.MetricsConsent != ConsentGranted {
		t.Errorf("round-trip = %+v, want id=abc123 granted", got)
	}
	if got.SchemaVersion != schemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, schemaVersion)
	}
}

func TestAnonymousIDUnique(t *testing.T) {
	a, b := newAnonymousID(), newAnonymousID()
	if a == "" || a == b {
		t.Errorf("expected distinct non-empty ids, got %q and %q", a, b)
	}
}

func TestOptedOut(t *testing.T) {
	for _, v := range []string{"DRIVERFORGE_NO_METRICS", "DO_NOT_TRACK", "CI"} {
		isolateConfig(t)
		t.Setenv(v, "1")
		if !optedOut() {
			t.Errorf("optedOut() = false with %s set", v)
		}
	}
	isolateConfig(t)
	if optedOut() {
		t.Error("optedOut() = true with no opt-out env set")
	}
}

func TestClientDisabledWithoutConsent(t *testing.T) {
	isolateConfig(t)
	c := New(Config{MetricsConsent: ConsentUnset})
	if c.Enabled() {
		t.Error("client enabled without consent")
	}
	// Disabled client buffers nothing and Flush is a harmless no-op.
	c.Track(BusinessEvent{Name: "build"})
	c.Flush(context.Background())
}

func TestClientDisabledWhenOptedOut(t *testing.T) {
	isolateConfig(t)
	t.Setenv("DO_NOT_TRACK", "1")
	c := New(Config{MetricsConsent: ConsentGranted})
	if c.Enabled() {
		t.Error("client enabled despite DO_NOT_TRACK")
	}
}

func TestNilClientSafe(t *testing.T) {
	var c *Client
	if c.Enabled() {
		t.Error("nil client should report disabled")
	}
	c.Track(BusinessEvent{Name: "build"}) // must not panic
	c.Flush(context.Background())
}

func TestEnsureConsentNonInteractive(t *testing.T) {
	isolateConfig(t)
	// Non-TTY test environment: must not prompt or block, and must not persist.
	cfg := EnsureConsent()
	if cfg.MetricsConsent != ConsentUnset {
		t.Errorf("non-interactive consent = %q, want unset", cfg.MetricsConsent)
	}
	if Load().MetricsConsent != ConsentUnset {
		t.Error("non-interactive EnsureConsent must not persist a decision")
	}
}

func TestMixpanelSinkInert(t *testing.T) {
	s := &mixpanelSink{token: "", url: "http://example.invalid"}
	if err := s.Send(context.Background(), Identity{DeviceID: "id"}, []BusinessEvent{{Name: "build"}}); err != nil {
		t.Errorf("inert mixpanel sink returned error: %v", err)
	}
}

// mixpanelCapture runs a test server and returns the first tracked event's
// name and properties after a Send.
func mixpanelCapture(t *testing.T, id Identity, events []BusinessEvent) (string, map[string]any) {
	t.Helper()
	var gotEvent string
	var gotProps map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		raw, _ := base64.StdEncoding.DecodeString(form.Get("data"))
		var payload []map[string]any
		_ = json.Unmarshal(raw, &payload)
		if len(payload) == 1 {
			gotEvent, _ = payload[0]["event"].(string)
			gotProps, _ = payload[0]["properties"].(map[string]any)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &mixpanelSink{token: "tok", url: srv.URL}
	if err := s.Send(context.Background(), id, events); err != nil {
		t.Fatalf("Send: %v", err)
	}
	return gotEvent, gotProps
}

func TestMixpanelSinkAnonymous(t *testing.T) {
	event, props := mixpanelCapture(t, Identity{DeviceID: "anon-1"}, []BusinessEvent{{
		Name:       "build",
		Properties: map[string]any{"env": "dev"},
	}})
	if event != "build" {
		t.Errorf("event = %q, want build", event)
	}
	if got := props["distinct_id"]; got != "$device:anon-1" {
		t.Errorf("distinct_id = %v, want $device:anon-1", got)
	}
	if got := props["$device_id"]; got != "anon-1" {
		t.Errorf("$device_id = %v, want anon-1", got)
	}
	if _, present := props["$user_id"]; present {
		t.Error("$user_id must be absent for a signed-out send")
	}
	if got := props["source"]; got != "cli" {
		t.Errorf("source = %v, want cli", got)
	}
}

func TestMixpanelSinkIdentified(t *testing.T) {
	_, props := mixpanelCapture(t, Identity{DeviceID: "anon-1", UserID: "sub-42", OrgID: "org-7"}, []BusinessEvent{{Name: "build"}})
	if got := props["distinct_id"]; got != "sub-42" {
		t.Errorf("distinct_id = %v, want sub-42", got)
	}
	if got := props["$user_id"]; got != "sub-42" {
		t.Errorf("$user_id = %v, want sub-42", got)
	}
	if got := props["$device_id"]; got != "anon-1" {
		t.Errorf("$device_id = %v, want anon-1", got)
	}
	if got := props["organization"]; got != "org-7" {
		t.Errorf("organization = %v, want org-7", got)
	}
}
