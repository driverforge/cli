package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/driverforge/cli/internal/agent"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
)

func TestAgentUnreachableErrIsFriendly(t *testing.T) {
	cause := errors.New("dial tcp 192.168.110.118:9443: connect: connection refused")
	dev := orgctx.Device{Name: "core1-000FFF9C4F48", IP: "192.168.110.118"}
	err := agentUnreachableErr(dev, cause)

	if !clierr.IsUser(err) {
		t.Fatalf("an unreachable agent must be a friendly UserError (not a crash), got %T", err)
	}
	if !errors.Is(err, cause) {
		t.Error("underlying cause should be preserved for errors.Is")
	}
	got := err.Error()
	for _, want := range []string{"core1-000FFF9C4F48", "192.168.110.118", agentDocsURL} {
		if !strings.Contains(got, want) {
			t.Errorf("message/hint missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, ":9443") {
		t.Errorf("the agent port should not be exposed to the user: %q", got)
	}
}

func TestAgentUnreachableErrSurfacesUserErrorCause(t *testing.T) {
	// A specific, anticipated cause (e.g. the pinned cert changed) must surface
	// its own card rather than being buried under the generic "Agent unreachable".
	cause := clierr.UserT("Controller certificate changed", "cert mismatch", "re-select it")
	got := agentUnreachableErr(orgctx.Device{Name: "Ctrl", IP: "1.2.3.4"}, cause)
	var ue *clierr.UserError
	if !errors.As(got, &ue) || ue.Title != "Controller certificate changed" {
		t.Errorf("a UserError cause should surface unchanged, got %#v", got)
	}

	// A raw transport error still gets the generic unreachable card.
	got = agentUnreachableErr(orgctx.Device{Name: "Ctrl", IP: "1.2.3.4"}, errors.New("connection refused"))
	if !errors.As(got, &ue) || ue.Title != "Agent unreachable" {
		t.Errorf("a raw cause should get the unreachable card, got %#v", got)
	}
}

func TestDialAgentRefreshesStaleFingerprint(t *testing.T) {
	// A TLS "agent" that answers /healthz.
	agentSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"agentVersion":"1.2.3","capabilities":["deploy"]}`)
	}))
	defer agentSrv.Close()
	sum := sha256.Sum256(agentSrv.Certificate().Raw)
	realFP := hex.EncodeToString(sum[:])

	// The graph returns the agent's ACTUAL fingerprint for the device — the state
	// after the agent was reinstalled and re-attested its new cert.
	graphSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"device":{"deviceId":"MAC1","certFingerprint":%q}}}`, realFP)
	}))
	defer graphSrv.Close()

	// Pinning ON (no DRIVERFORGE_AGENT_INSECURE); auth + graph + agent stubbed.
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRIVERFORGE_GRAPH_URL", graphSrv.URL)
	u, _ := url.Parse(agentSrv.URL)
	t.Setenv("DRIVERFORGE_AGENT_PORT", u.Port())

	if err := orgctx.SetCurrent(orgctx.Org{ID: "o1", Name: "Org"}); err != nil {
		t.Fatal(err)
	}
	// Cached with a STALE (wrong) fingerprint but a known DeviceID → self-heal.
	dev := orgctx.Device{ID: "d1", Name: "Ctrl", OrgID: "o1", DeviceID: "MAC1", IP: u.Hostname(), TLSFingerprint: strings.Repeat("ab", 32)}
	if err := orgctx.SetCurrentDevice(dev); err != nil {
		t.Fatal(err)
	}

	cl, h, err := dialAgent(context.Background(), dev)
	if err != nil {
		t.Fatalf("dialAgent should self-heal the stale fingerprint, got: %v", err)
	}
	if cl == nil || h.AgentVersion != "1.2.3" {
		t.Fatalf("expected a healthy client after refresh, got h=%+v", h)
	}
	// The refreshed fingerprint is persisted so later commands don't re-heal.
	got, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if got.TLSFingerprint != realFP {
		t.Errorf("refreshed fingerprint not persisted: got %q want %q", got.TLSFingerprint, realFP)
	}
}

func TestDialAgentRejectsFingerprintNotMatchingCert(t *testing.T) {
	// Security invariant: dialAgent only ever trusts the graph-attested
	// fingerprint, never the cert the controller happens to present. If the
	// graph's value doesn't match the controller's actual cert (a MITM presenting
	// a rogue cert, or a stale/poisoned graph record), the connection must still
	// be refused after the refresh — not blindly accepted.
	agentSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"agentVersion":"1.2.3"}`)
	}))
	defer agentSrv.Close()

	// The graph returns a fingerprint matching neither the cache nor the cert.
	graphSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"data":{"device":{"deviceId":"MAC1","certFingerprint":%q}}}`, strings.Repeat("cd", 32))
	}))
	defer graphSrv.Close()

	isolateConfig(t)
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DRIVERFORGE_GRAPH_URL", graphSrv.URL)
	u, _ := url.Parse(agentSrv.URL)
	t.Setenv("DRIVERFORGE_AGENT_PORT", u.Port())
	if err := orgctx.SetCurrent(orgctx.Org{ID: "o1"}); err != nil {
		t.Fatal(err)
	}
	dev := orgctx.Device{ID: "d1", Name: "Ctrl", OrgID: "o1", DeviceID: "MAC1", IP: u.Hostname(), TLSFingerprint: strings.Repeat("ab", 32)}
	if err := orgctx.SetCurrentDevice(dev); err != nil {
		t.Fatal(err)
	}

	if _, _, err := dialAgent(context.Background(), dev); !errors.Is(err, agent.ErrFingerprintMismatch) {
		t.Errorf("a graph fingerprint that doesn't match the cert must still be refused, got %v", err)
	}
}

func TestAgentMissingCapabilityErr(t *testing.T) {
	err := agentMissingCapabilityErr(orgctx.Device{Name: "core1"}, "deploy", "1.2.3")
	if !clierr.IsUser(err) {
		t.Fatalf("out-of-date agent must be a friendly UserError, got %T", err)
	}
	got := err.Error()
	for _, want := range []string{"deploy", "1.2.3", agentDocsURL} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q: %q", want, got)
		}
	}
}

// TestRunDeployPlainStreamsProgress drives the non-interactive deploy against an
// agent streaming the NDJSON deploy protocol, asserting the transcript and that
// the structural hash we sent reached the agent. (runDeploy itself goes through
// the graph for device resolution; runDeployPlain is the streamed core.)
func TestRunDeployPlainStreamsProgress(t *testing.T) {
	var sentHash, gotOptIn string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sentHash = r.Header.Get("X-Driver-Structural-Hash")
		gotOptIn = r.Header.Get("X-Deploy-Stream")
		io.WriteString(w, `{"state":"staged","step":"driver written to the controller","terminal":false}
{"state":"reinstalling","step":"director reinstalling the driver","terminal":false}
{"state":"restarting","step":"driver restarting","terminal":false}
{"state":"confirmed","step":"driver restarted","version":"1.2.3","terminal":true}
`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv) // stubs auth + DRIVERFORGE_AGENT_INSECURE (from sync_integration_test.go)

	cl := agent.New(srv.URL, "")
	dev := orgctx.Device{ID: "d1", Name: "Ctrl"}

	var out bytes.Buffer
	if _, err := runDeployPlain(context.Background(), &out, cl, dev, "test.c4z", "sha256:sent", strings.NewReader("c4zbytes"), true, "0.0.0", "9.9.9"); err != nil {
		t.Fatalf("runDeployPlain: %v", err)
	}
	transcript := out.String()
	for _, want := range []string{"driver written to the controller", "director reinstalling the driver", "Version 1.2.3 deployed"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript missing %q:\n%s", want, transcript)
		}
	}
	if sentHash != "sha256:sent" {
		t.Errorf("agent received structural hash %q, want sha256:sent", sentHash)
	}
	if gotOptIn != "1" {
		t.Errorf("CLI should opt into streaming (X-Deploy-Stream), got %q", gotOptIn)
	}
}

// A reachable agent that streams a terminal `failed` → a friendly UserError, exit
// non-zero (not a fake success), carrying the report link.
func TestRunDeployPlainFail(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"staged","step":"writing driver","terminal":false}
{"state":"failed","step":"driver failed to load","error":"driver crashed during init","reportUrl":"https://app.driverforge.dev/reports/xyz","terminal":true}
`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)

	cl := agent.New(srv.URL, "")
	var out bytes.Buffer
	_, err := runDeployPlain(context.Background(), &out, cl, orgctx.Device{ID: "d1", Name: "Ctrl"}, "test.c4z", "h", strings.NewReader("c4z"), true, "0.0.0", "")
	if err == nil {
		t.Fatal("a terminal `failed` must surface as an error, not a fake success")
	}
	if !clierr.IsUser(err) {
		t.Errorf("a driver-crash deploy failure should be a friendly UserError, got %T", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "driver crashed during init") || !strings.Contains(msg, "reports/xyz") {
		t.Errorf("failure error should carry the message + report link: %q", msg)
	}
}

// confirmed_with_errors: the driver is running, so exit 0 — but show the init
// error + report link.
func TestRunDeployPlainConfirmedWithErrors(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"state":"restarting","step":"driver restarting","terminal":false}
{"state":"confirmed_with_errors","step":"driver restarted","version":"2.0.0","error":"OnDriverInit: nil deref","reportUrl":"https://app.driverforge.dev/reports/init1","terminal":true}
`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)

	cl := agent.New(srv.URL, "")
	var out bytes.Buffer
	if _, err := runDeployPlain(context.Background(), &out, cl, orgctx.Device{ID: "d1", Name: "Ctrl"}, "test.c4z", "h", strings.NewReader("c4z"), true, "0.0.0", ""); err != nil {
		t.Fatalf("confirmed_with_errors should still exit 0 (driver is running): %v", err)
	}
	s := out.String()
	for _, want := range []string{"OnDriverInit: nil deref", "reports/init1", "Version 2.0.0 deployed"} {
		if !strings.Contains(s, want) {
			t.Errorf("transcript missing %q:\n%s", want, s)
		}
	}
}

// An agent rejection (non-200 with a reason) is a friendly user/setup condition
// — a card, never a crash + Sentry prompt — surfacing the agent's message.
func TestRunDeployPlainAgentRejectionIsFriendly(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"director_busy","message":"the director is mid-update; try again shortly"}`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)

	cl := agent.New(srv.URL, "")
	_, err := runDeployPlain(context.Background(), &bytes.Buffer{}, cl, orgctx.Device{ID: "d1", Name: "Ctrl"}, "x.c4z", "h", strings.NewReader("c4z"), true, "0.0.0", "")
	if err == nil {
		t.Fatal("expected the agent rejection to surface as an error")
	}
	if !clierr.IsUser(err) {
		t.Errorf("an agent rejection should be a friendly UserError (not a crash), got %T", err)
	}
	if !strings.Contains(err.Error(), "director is mid-update") {
		t.Errorf("the agent's message should be surfaced: %q", err.Error())
	}
}

// When the agent rejects because the driver isn't in the project (and it can't
// bootstrap), the CLI shows a deliberate "set it up in Composer Pro" warning,
// not the agent's raw wording and not a crash.
func TestRunDeployPlainNotInProjectGuidesToComposer(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"not_in_project","message":"driver x.c4z is not in the project"}`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)

	cl := agent.New(srv.URL, "")
	_, err := runDeployPlain(context.Background(), &bytes.Buffer{}, cl, orgctx.Device{ID: "d1", Name: "Ctrl"}, "x.c4z", "h", strings.NewReader("c4z"), true, "0.0.0", "")
	if !clierr.IsUser(err) {
		t.Fatalf("not-in-project should be a friendly UserError, got %T", err)
	}
	got := err.Error()
	for _, want := range []string{"Ctrl", "Composer Pro", "driverforge deploy"} {
		if !strings.Contains(got, want) {
			t.Errorf("Composer-first guidance missing %q: %q", want, got)
		}
	}
}

func TestDriverVersionFromC4z(t *testing.T) {
	dir := t.TempDir()

	verOnly := filepath.Join(dir, "v.c4z")
	writeC4z(t, verOnly, xmlV1, "print('lua')")
	if got := driverVersionFromC4z(verOnly); got != "000001" {
		t.Errorf("version-only = %q, want 000001", got)
	}

	if got := driverVersionFromC4z(filepath.Join(dir, "missing.c4z")); got != "" {
		t.Errorf("missing .c4z = %q, want empty", got)
	}
}

// The stream deadline expiring is a TIMEOUT, not a user cancel — "Cancelled —
// No changes were made" would be a lie when the driver is already staged.
func TestClassifyDeployRunErr(t *testing.T) {
	dev := orgctx.Device{Name: "Theatre EA-5"}
	cases := []struct {
		name      string
		err       error
		wantTitle string
	}{
		// tea kills the program with ErrProgramKilled wrapping the context error.
		{"deadline via killed program", fmt.Errorf("%w: %w", tea.ErrProgramKilled, context.DeadlineExceeded), "Deploy timed out"},
		{"bare deadline", context.DeadlineExceeded, "Deploy timed out"},
		{"ctrl+c interrupt", tea.ErrInterrupted, "Cancelled"},
		{"killed without deadline", tea.ErrProgramKilled, "Cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyDeployRunErr(dev, tc.err)
			var ue *clierr.UserError
			if !errors.As(got, &ue) {
				t.Fatalf("classifyDeployRunErr(%v) = %v, want a clierr.UserError", tc.err, got)
			}
			if ue.Title != tc.wantTitle {
				t.Errorf("title = %q, want %q", ue.Title, tc.wantTitle)
			}
		})
	}
}

func TestDeployConfirmed(t *testing.T) {
	cases := map[agent.DeployState]bool{
		agent.StateConfirmed:           true,
		agent.StateConfirmedWithErrors: true,
		agent.StateUnconfirmed:         false,
		agent.StateFailed:              false,
		agent.StateStaged:              false,
		"":                             false,
	}
	for state, want := range cases {
		if got := deployConfirmed(state); got != want {
			t.Errorf("deployConfirmed(%q) = %v, want %v", state, got, want)
		}
	}
}

// runDeployPlain reports the terminal state so the caller can gate the sync
// baseline: an unconfirmed restart must not be cached (the old, structurally
// different driver may still be running).
func TestRunDeployPlainReportsState(t *testing.T) {
	cases := []struct {
		name, stream string
		want         agent.DeployState
	}{
		{"confirmed", `{"state":"confirmed","step":"driver reloaded","terminal":true}`, agent.StateConfirmed},
		{"unconfirmed", `{"state":"unconfirmed","step":"couldn't confirm","hint":"check it","terminal":true}`, agent.StateUnconfirmed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				io.WriteString(w, tc.stream+"\n")
			}))
			t.Cleanup(srv.Close)
			setupSyncEnv(t, srv)

			cl := agent.New(srv.URL, "")
			state, err := runDeployPlain(context.Background(), &bytes.Buffer{}, cl,
				orgctx.Device{ID: "d1", Name: "Ctrl"}, "x.c4z", "h", strings.NewReader("c4z"), true, "0.0.0", "")
			if err != nil {
				t.Fatalf("runDeployPlain: %v", err)
			}
			if state != tc.want {
				t.Errorf("state = %q, want %q", state, tc.want)
			}
			if got := deployConfirmed(state); got != (tc.want == agent.StateConfirmed) {
				t.Errorf("deployConfirmed gate wrong for %q", tc.want)
			}
		})
	}
}

// persistFingerprint caches a freshly-fetched fingerprint onto the current
// device, so later probes skip the graph round-trip — but only when it's
// actually the current device (a --device probe against another controller
// must not overwrite the selection's record).
func TestPersistFingerprint(t *testing.T) {
	isolateConfig(t)
	if err := orgctx.SetCurrent(orgctx.Org{ID: "o1", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := orgctx.SetCurrentDevice(orgctx.Device{ID: "d1", Name: "Ctrl", OrgID: "o1"}); err != nil {
		t.Fatal(err)
	}

	// Persisting for the current device writes the fingerprint back.
	persistFingerprint(orgctx.Device{ID: "d1", Name: "Ctrl", OrgID: "o1"}, "sha256:abc")
	got, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if got.TLSFingerprint != "sha256:abc" {
		t.Errorf("fingerprint not persisted: %q", got.TLSFingerprint)
	}

	// Persisting for a DIFFERENT device id must not touch the current record.
	persistFingerprint(orgctx.Device{ID: "other", Name: "Elsewhere", OrgID: "o1"}, "sha256:zzz")
	got, _ = orgctx.CurrentDevice()
	if got.TLSFingerprint != "sha256:abc" {
		t.Errorf("a non-current device overwrote the selection: %q", got.TLSFingerprint)
	}
}
