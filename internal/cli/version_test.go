package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/driverforge/cli/internal/buildinfo"
)

// The root --version flag must print the same CLI build line as the `version`
// subcommand's first line and exit — it short-circuits before the consent
// prompt and update check, so it stays safe for scripts and shell prompts.
func TestRootVersionFlag(t *testing.T) {
	var out bytes.Buffer
	cmd := newRootCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("--version: %v", err)
	}
	if got, want := out.String(), buildinfo.String()+"\n"; got != want {
		t.Errorf("output = %q, want %q", got, want)
	}
}

// The controller's agent + Director group under the controller's own name —
// no collective "component" label, no MAC or other controller details.
func TestPrintVersionGroupsUnderController(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s, want /healthz", r.URL.Path)
		}
		io.WriteString(w, `{"ok":true,"agentVersion":"0.1.2","controllerVersion":"4.0.0.734549","controllerName":"core1-000FFF9C4F48","controllerMac":"00:0F:FF:9C:4F:48","protocol":4,"capabilities":["deploy"]}`)
	}))
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv) // sets the current device "Ctrl" -> this server

	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/live" {
			t.Errorf("graph path = %s, want /health/live", r.URL.Path)
		}
		io.WriteString(w, `{"status":"ok","release":"sha-abc1234"}`)
	}))
	t.Cleanup(graph.Close)
	t.Setenv("DRIVERFORGE_GRAPH_URL", graph.URL+"/graphql")

	var out bytes.Buffer
	runVersion(context.Background(), &out)
	s := out.String()
	for _, want := range []string{"Driverforge\n", "  CLI", "  Cloud", "sha-abc1234", "\ncore1-000FFF9C4F48\n", "  Anvil Agent", "0.1.2", "  Director", "4.0.0.734549"} {
		if !strings.Contains(s, want) {
			t.Errorf("version output missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "4F:48") {
		t.Errorf("the MAC should be gone from version output:\n%s", s)
	}
	if strings.Contains(s, "COMPONENT") {
		t.Errorf("no collective header wanted:\n%s", s)
	}
}

func TestPrintVersionUnreachable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	setupSyncEnv(t, srv)
	srv.Close()                                                     // selected, but now unreachable
	t.Setenv("DRIVERFORGE_GRAPH_URL", "http://127.0.0.1:1/graphql") // no graph either

	var out bytes.Buffer
	runVersion(context.Background(), &out)
	if !strings.Contains(out.String(), "unreachable") {
		t.Errorf("an unreachable controller should note it, got %q", out.String())
	}
}

// With nothing in context (no project, no controller), version is just the
// Driverforge group.
func TestPrintVersionNoContext(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("DRIVERFORGE_GRAPH_URL", "http://127.0.0.1:1/graphql") // no graph reachable

	var out bytes.Buffer
	runVersion(context.Background(), &out)
	s := out.String()
	if !strings.Contains(s, "Driverforge\n") || !strings.Contains(s, "  CLI") {
		t.Errorf("expected just the Driverforge group:\n%s", s)
	}
	if strings.Contains(s, "Anvil") || strings.Contains(s, "Cloud") {
		t.Errorf("no SDK/agent/graph rows expected with nothing in context:\n%s", s)
	}
}
