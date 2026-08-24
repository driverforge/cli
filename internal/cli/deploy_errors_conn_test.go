package cli

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
)

func testDevice() orgctx.Device {
	return orgctx.Device{ID: "d1", Name: "core1-000FFF9C4F48", IP: "192.168.110.118"}
}

// realGet performs an actual HTTP request so the classifier is exercised against
// what net/http genuinely returns (a *url.Error wrapping a *net.OpError), not a
// hand-built error that happens to match.
func realGet(t *testing.T, url string, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: timeout}).Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the request to fail")
	}
	return err
}

// A port nothing is bound to: the controller answers, nothing is serving. This
// is the reinstalled-agent case — its deploy server fails closed without a
// certificate and never restarts, so the port is closed while the host is fine.
func TestAgentUnreachableConnectionRefused(t *testing.T) {
	// Bind then close, so the port is known-free and nothing races onto it.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()

	cause := realGet(t, "http://"+addr+"/healthz", 5*time.Second)
	if !isConnRefused(cause) {
		t.Fatalf("a refused connection was not classified as such: %v", cause)
	}

	var ue *clierr.UserError
	if !errors.As(agentUnreachableErr(testDevice(), cause), &ue) {
		t.Fatal("want a UserError")
	}
	if ue.Title != "Agent not accepting connections" {
		t.Errorf("title = %q", ue.Title)
	}
	// The recourse that was missing: the action that actually fixes it, and a
	// link for the why. The hint deliberately does NOT explain the agent's
	// internals — that reasoning changes when the agent does; the docs carry it.
	if !strings.Contains(ue.Hint, "Restart Deploy Server") {
		t.Errorf("hint doesn't name the Composer action: %q", ue.Hint)
	}
	if !strings.Contains(ue.Hint, agentDocsURL) {
		t.Errorf("hint doesn't link the docs: %q", ue.Hint)
	}
	if !strings.Contains(ue.Message, "nothing is listening") {
		t.Errorf("message should distinguish this from an unreachable host: %q", ue.Message)
	}
}

// Nothing answers in time: a different problem with different recourse.
func TestAgentUnreachableTimeout(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3 — reserved, routed nowhere, so it hangs.
	cause := realGet(t, "http://203.0.113.1:9999/healthz", 300*time.Millisecond)
	if !isTimeout(cause) {
		t.Skipf("environment returned a non-timeout for a black-holed address: %v", cause)
	}

	var ue *clierr.UserError
	if !errors.As(agentUnreachableErr(testDevice(), cause), &ue) {
		t.Fatal("want a UserError")
	}
	if ue.Title != "Controller not responding" {
		t.Errorf("title = %q", ue.Title)
	}
	if strings.Contains(ue.Hint, "Restart Deploy Server") {
		t.Error("a timeout must not send the user to restart a server they can't reach")
	}
	if !strings.Contains(ue.Hint, "device select") {
		t.Errorf("hint should offer re-selecting a changed address: %q", ue.Hint)
	}
}

// A name that won't resolve — reachable when a device record has a hostname and
// no IP, since deviceHost prefers the IP.
func TestAgentUnreachableDNS(t *testing.T) {
	cause := realGet(t, "http://core1-000fff9c4f48.invalid:9999/healthz", 5*time.Second)
	if !isDNSError(cause) {
		t.Skipf("resolver did not return a DNS error (captive DNS?): %v", cause)
	}
	var ue *clierr.UserError
	if !errors.As(agentUnreachableErr(testDevice(), cause), &ue) {
		t.Fatal("want a UserError")
	}
	if ue.Title != "Controller address won't resolve" {
		t.Errorf("title = %q", ue.Title)
	}
}

// An unrecognised transport fault still gets the generic card rather than being
// mis-sorted into one of the specific ones.
func TestAgentUnreachableFallsBackToGeneric(t *testing.T) {
	var ue *clierr.UserError
	if !errors.As(agentUnreachableErr(testDevice(), errors.New("something else")), &ue) {
		t.Fatal("want a UserError")
	}
	if ue.Title != "Agent unreachable" {
		t.Errorf("title = %q, want the generic card", ue.Title)
	}
}

// A condition that already carries its own guidance (the pinned certificate
// changed) must not be flattened into a connection card.
func TestAgentUnreachablePassesThroughUserErrors(t *testing.T) {
	in := clierr.UserT("Controller certificate changed", "msg", "hint")
	if got := agentUnreachableErr(testDevice(), in); got != error(in) {
		t.Errorf("a UserError cause was not surfaced unchanged: %v", got)
	}
}
