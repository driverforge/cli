package telemetry

import (
	"encoding/json"
	"testing"
)

func TestIsDriverEvent(t *testing.T) {
	// Any driver-fired event counts (OnDriverInit often isn't captured; the real
	// first signal is OnDriverLateInit / OnPropertyChanged / etc.). Logs and
	// heartbeats don't.
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"onDriverInit event", `{"type":"event","name":"OnDriverInit","traceId":"t1"}`, true},
		{"onDriverLateInit event", `{"type":"event","name":"OnDriverLateInit"}`, true},
		{"onPropertyChanged event", `{"type":"event","name":"OnPropertyChanged"}`, true},
		{"log, not event", `{"type":"log","name":""}`, false},
		{"heartbeat, not event", `{"type":"heartbeat"}`, false},
		{"error, not event", `{"type":"error"}`, false},
		{"empty", `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e Event
			if err := json.Unmarshal([]byte(tc.raw), &e); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got := IsDriverEvent(e); got != tc.want {
				t.Errorf("IsDriverEvent(%s) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestEventUnmarshalKeepsFields(t *testing.T) {
	var e Event
	raw := `{"type":"event","name":"OnDriverInit","traceId":"abc123","driver":"my-driver","controller":"000FFF","tags":{"controller":"living-room"}}`
	if err := json.Unmarshal([]byte(raw), &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if e.TraceID != "abc123" || e.Driver != "my-driver" || e.Controller != "000FFF" || e.Tags["controller"] != "living-room" {
		t.Errorf("fields not parsed: %+v", e)
	}
}

func TestEventsURL(t *testing.T) {
	// Release builds carry a short-link base: both slugs ride in the query string
	// for the app's /a handler to unpack.
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "https://go.driverforge.test/")
	got := EventsURL("acme", "thermostat")
	want := "https://go.driverforge.test/events?org=acme&project=thermostat"
	if got != want {
		t.Errorf("EventsURL = %q, want %q", got, want)
	}

	// Both slugs ride in the query string, so anything needing escaping must not
	// be able to break out of its parameter and forge another one.
	if got, want := EventsURL("acme", "a b&org=evil"), "https://go.driverforge.test/events?org=acme&project=a+b%26org%3Devil"; got != want {
		t.Errorf("EventsURL escaping = %q, want %q", got, want)
	}
}

// With no short-link base (local/dev/fork builds) the link points straight at the
// app — the same destination /a would have redirected to, so no /a route is needed.
func TestEventsURLDirect(t *testing.T) {
	t.Setenv("DRIVERFORGE_SHORTLINK_URL", "")
	t.Setenv("DRIVERFORGE_APP_URL", "https://dev.driverforge.test/")

	if got, want := EventsURL("acme", "thermostat"), "https://dev.driverforge.test/acme/events/?project=thermostat"; got != want {
		t.Errorf("direct EventsURL = %q, want %q", got, want)
	}
	if got, want := EventsURL("acme", "a b&x=1"), "https://dev.driverforge.test/acme/events/?project=a+b%26x%3D1"; got != want {
		t.Errorf("direct EventsURL escaping = %q, want %q", got, want)
	}
}

func TestResolveCentrifugoOrigin(t *testing.T) {
	// Derived from the app URL by default (scheme + host, no path/trailing slash).
	t.Setenv("DRIVERFORGE_CENTRIFUGO_ORIGIN", "")
	t.Setenv("DRIVERFORGE_APP_URL", "https://dev.driverforge.test/foo/")
	if got := resolveCentrifugoOrigin(); got != "https://dev.driverforge.test" {
		t.Errorf("origin = %q, want https://dev.driverforge.test", got)
	}
	// Explicit override wins.
	t.Setenv("DRIVERFORGE_CENTRIFUGO_ORIGIN", "https://custom.example")
	if got := resolveCentrifugoOrigin(); got != "https://custom.example" {
		t.Errorf("override origin = %q, want https://custom.example", got)
	}
}

func TestChannel(t *testing.T) {
	if got := channel("proj-1"); got != "project:proj-1" {
		t.Errorf("channel = %q, want project:proj-1", got)
	}
}
