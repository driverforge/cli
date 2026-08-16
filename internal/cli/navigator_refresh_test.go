package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeRefreshAgent serves a legacy one-shot /deploy plus /refresh-navigators,
// recording refresh hits, with the navigator capability toggleable.
type fakeRefreshAgent struct {
	mu          sync.Mutex
	noCap       bool // omit refresh-navigators from /healthz
	refreshHits int
}

func (f *fakeRefreshAgent) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refreshHits
}

func (f *fakeRefreshAgent) start(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		caps := `["deploy","refresh-navigators"]`
		if f.noCap {
			caps = `["deploy"]`
		}
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true,"agentVersion":"0.1.0","protocol":5,"capabilities":`+caps+`}`)
	})
	mux.HandleFunc("/deploy", func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("X-Driver-Structural-Hash")
		io.WriteString(w, `{"ok":true,"structuralHash":"`+h+`"}`)
	})
	mux.HandleFunc("/refresh-navigators", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		f.refreshHits++
		f.mu.Unlock()
		io.WriteString(w, `{"ok":true}`)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// A confirmed deploy refreshes navigators when the structural hash changed
// since the device's baseline (including a first deploy), skips the refresh
// when unchanged, and --refresh-navigators forces it.
func TestDeployRefreshesNavigatorsOnStructuralChange(t *testing.T) {
	fa := &fakeRefreshAgent{}
	setupSyncEnv(t, fa.start(t))
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	c4z := filepath.Join(t.TempDir(), "test.c4z")
	writeC4z(t, c4z, xmlV1, "print('v1')")

	// First deploy: no baseline → refresh.
	var out bytes.Buffer
	if err := runDeployToDevice(context.Background(), &out, mustCurrentDevice(t), c4z, "test", false); err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if fa.hits() != 1 {
		t.Fatalf("first deploy should refresh navigators, hits=%d", fa.hits())
	}
	if !strings.Contains(out.String(), "Navigators refreshed") {
		t.Errorf("output should report the refresh:\n%s", out.String())
	}

	// Redeploy the same build: baseline matches → no refresh.
	out.Reset()
	if err := runDeployToDevice(context.Background(), &out, mustCurrentDevice(t), c4z, "test", false); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	if fa.hits() != 1 {
		t.Fatalf("unchanged redeploy must not refresh, hits=%d", fa.hits())
	}
	if strings.Contains(out.String(), "Navigators refreshed") {
		t.Errorf("unchanged redeploy should stay quiet about navigators:\n%s", out.String())
	}

	// Same build with --refresh-navigators: forced.
	if err := runDeployToDevice(context.Background(), &out, mustCurrentDevice(t), c4z, "test", true); err != nil {
		t.Fatalf("forced redeploy: %v", err)
	}
	if fa.hits() != 2 {
		t.Fatalf("--refresh-navigators must force the refresh, hits=%d", fa.hits())
	}

	// A structural change flips the hash → refresh.
	writeC4z(t, c4z, xmlV2, "print('v2')")
	if err := runDeployToDevice(context.Background(), &out, mustCurrentDevice(t), c4z, "test", false); err != nil {
		t.Fatalf("changed redeploy: %v", err)
	}
	if fa.hits() != 3 {
		t.Fatalf("a structural change must refresh, hits=%d", fa.hits())
	}
}

// An agent without the capability is skipped with a note — never called, and
// the deploy still succeeds.
func TestDeploySkipsRefreshWithoutCapability(t *testing.T) {
	fa := &fakeRefreshAgent{noCap: true}
	setupSyncEnv(t, fa.start(t))
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	c4z := filepath.Join(t.TempDir(), "test.c4z")
	writeC4z(t, c4z, xmlV1, "print('v1')")

	var out bytes.Buffer
	if err := runDeployToDevice(context.Background(), &out, mustCurrentDevice(t), c4z, "test", false); err != nil {
		t.Fatalf("runDeploy: %v", err)
	}
	if fa.hits() != 0 {
		t.Fatalf("must not call an agent without the capability, hits=%d", fa.hits())
	}
	if !strings.Contains(out.String(), "navigators not refreshed") {
		t.Errorf("output should note the skipped refresh:\n%s", out.String())
	}
}

// The decision logic in isolation.
func TestShouldRefreshNavigators(t *testing.T) {
	cases := []struct {
		name      string
		force     bool
		prev, new string
		want      bool
	}{
		{"first deploy (no baseline)", false, "", "h1", true},
		{"unchanged", false, "h1", "h1", false},
		{"changed", false, "h1", "h2", true},
		{"forced despite unchanged", true, "h1", "h1", true},
	}
	for _, c := range cases {
		if got := shouldRefreshNavigators(c.force, c.prev, c.new); got != c.want {
			t.Errorf("%s: shouldRefreshNavigators = %v, want %v", c.name, got, c.want)
		}
	}
}

// The standalone command triggers the refresh against the remembered device.
func TestDeviceRefreshNavigatorsCommand(t *testing.T) {
	fa := &fakeRefreshAgent{}
	setupSyncEnv(t, fa.start(t))
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	cmd := newDeviceRefreshNavigatorsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("refresh-navigators: %v", err)
	}
	if fa.hits() != 1 {
		t.Fatalf("hits = %d, want 1", fa.hits())
	}
	if !strings.Contains(out.String(), "Navigators refreshed") {
		t.Errorf("output should confirm the refresh:\n%s", out.String())
	}
}

// The standalone command refuses cleanly when the agent is too old.
func TestDeviceRefreshNavigatorsCommandNeedsCapability(t *testing.T) {
	fa := &fakeRefreshAgent{noCap: true}
	setupSyncEnv(t, fa.start(t))
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	cmd := newDeviceRefreshNavigatorsCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "navigator refresh") {
		t.Fatalf("expected the missing-capability card, got %v", err)
	}
	if fa.hits() != 0 {
		t.Fatalf("must not call the endpoint, hits=%d", fa.hits())
	}
}

// device capabilities diagnoses both directions: what's advertised, what a
// known capability being absent looks like, and future capabilities this CLI
// doesn't know.
func TestDeviceCapabilitiesCommand(t *testing.T) {
	fa := &fakeRefreshAgent{} // advertises deploy + refresh-navigators only
	setupSyncEnv(t, fa.start(t))
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	cmd := newDeviceCapabilitiesCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	s := out.String()
	// Styled segments put ANSI codes between marker and name, so assert on
	// pieces that don't cross a style boundary.
	for _, want := range []string{"protocol 5", "full driver deploy", "refresh-navigators", "navigator refresh after UI changes", "not advertised"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q:\n%s", want, s)
		}
	}
	// The advertised rows carry ✓, the missing ones –.
	if !strings.Contains(s, "✓") || !strings.Contains(s, "–") {
		t.Errorf("output should mix advertised and missing markers:\n%s", s)
	}
}

// A capability the CLI doesn't know is flagged rather than hidden.
func TestDeviceCapabilitiesCommandUnknownCapability(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"agentVersion":"9.9","protocol":6,"capabilities":["deploy","quantum-teleport"]}`)
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)
	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	cmd := newDeviceCapabilitiesCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("capabilities: %v", err)
	}
	if !strings.Contains(out.String(), "quantum-teleport") || !strings.Contains(out.String(), "unknown to this CLI") {
		t.Errorf("unknown capability should be flagged:\n%s", out.String())
	}
}
