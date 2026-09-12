package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/orgctx"
)

// healthzDown makes healthzServing refuse a poll instead of answering it —
// the agent is down mid-restart.
const healthzDown = "\x00down"

// healthzServing stands up a /healthz endpoint whose reported agentVersion is
// supplied per request, and returns a client pointed at it plus a hit counter.
func healthzServing(t *testing.T, version func(n int) string) (*agent.Client, func() int) {
	t.Helper()
	var mu sync.Mutex
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		v := version(n)
		mu.Unlock()
		if v == healthzDown {
			http.Error(w, "agent restarting", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprintf(w, `{"agentVersion":%q,"protocol":4}`, v)
	}))
	t.Cleanup(srv.Close)
	return agent.New(srv.URL, ""), func() int { mu.Lock(); defer mu.Unlock(); return n }
}

// The agent came back on the new build: the version flipped, so the deploy is
// confirmed and carries the version the agent now reports.
func TestResolveSelfDeployConfirmsOnVersionFlip(t *testing.T) {
	cl, hits := healthzServing(t, func(int) string { return "2026080301" })

	got := resolveSelfDeploy(context.Background(), cl, orgctx.Device{}, "2026072104")

	if got.State != agent.StateConfirmed {
		t.Fatalf("State = %v, want %v", got.State, agent.StateConfirmed)
	}
	if got.Version != "2026080301" {
		t.Errorf("Version = %q, want the agent's newly reported version", got.Version)
	}
	if !got.Terminal {
		t.Error("a resolved self-deploy must be terminal")
	}
	if hits() != 1 {
		t.Errorf("expected 1 health poll, got %d", hits())
	}
}

// The agent is mid-restart: /healthz refuses, then answers with the old version,
// then finally flips. The poll must keep going rather than resolve on the first
// answer — settling early would report the pre-deploy build as the outcome.
func TestResolveSelfDeployPollsUntilVersionFlips(t *testing.T) {
	cl, hits := healthzServing(t, func(n int) string {
		switch n {
		case 1:
			return healthzDown // agent is down mid-restart
		case 2:
			return "2026072104" // back up, but still the old build
		default:
			return "2026080301"
		}
	})

	got := resolveSelfDeploy(context.Background(), cl, orgctx.Device{}, "2026072104")

	if got.State != agent.StateConfirmed {
		t.Fatalf("State = %v, want %v", got.State, agent.StateConfirmed)
	}
	if got.Version != "2026080301" {
		t.Errorf("Version = %q, want 2026080301", got.Version)
	}
	if n := hits(); n < 3 {
		t.Errorf("expected at least 3 polls before the flip, got %d", n)
	}
}

// The window closed without the version flipping. That is not a failure — the
// driver may well have installed — so it resolves unconfirmed, terminal, with a
// hint telling the user to re-check rather than a scary error.
func TestResolveSelfDeployUnconfirmedWhenWindowCloses(t *testing.T) {
	// A cancelled context closes the poll window immediately; waiting out the
	// real 90s timeout would exercise the same return for 90s of test time.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cl, _ := healthzServing(t, func(int) string { return "2026080301" })

	got := resolveSelfDeploy(ctx, cl, orgctx.Device{}, "2026072104")

	if got.State != agent.StateUnconfirmed {
		t.Fatalf("State = %v, want %v", got.State, agent.StateUnconfirmed)
	}
	if !got.Terminal {
		t.Error("an unconfirmed self-deploy must still be terminal")
	}
	if got.Hint == "" {
		t.Error("the unconfirmed outcome must carry a re-check hint")
	}
	if got.Version != "" {
		t.Errorf("Version = %q, want empty when nothing was confirmed", got.Version)
	}
}

// An agent that answers without a version tells us nothing — it must not be
// read as a flip away from the old build.
func TestResolveSelfDeployIgnoresEmptyVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cl, _ := healthzServing(t, func(int) string { return "" }) // healthy, but reports no version

	got := resolveSelfDeploy(ctx, cl, orgctx.Device{}, "2026072104")

	if got.State == agent.StateConfirmed {
		t.Fatalf("an empty agentVersion must not confirm the deploy (got %+v)", got)
	}
	if got.State != agent.StateUnconfirmed {
		t.Errorf("State = %v, want %v", got.State, agent.StateUnconfirmed)
	}
}

// The unchanged version is the real self-deploy hazard: the agent answers
// healthily on the OLD build, and treating that as success would report a
// deploy that never took effect.
func TestResolveSelfDeployDoesNotConfirmUnchangedVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cl, _ := healthzServing(t, func(int) string { return "2026072104" })

	got := resolveSelfDeploy(ctx, cl, orgctx.Device{}, "2026072104")

	if got.State == agent.StateConfirmed {
		t.Fatalf("an unchanged version must not confirm the deploy (got %+v)", got)
	}
}
