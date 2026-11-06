package cli

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/driverforge/cli/internal/agent"
	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/ui"
)

// The agent serves a certificate for a new key after self-deploy (a
// re-enrolment: a reissue for the same key keeps the pin). Graph is the only
// authority for its replacement pin; tests can deliberately make it disagree.
func pinRecoveryFixture(t *testing.T, rotateOnDeploy, graphMatches bool) (orgctx.Device, *agent.Client) {
	t.Helper()
	isolateConfig(t)
	t.Setenv("DRIVERFORGE_AGENT_INSECURE", "")
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	var active atomic.Pointer[tls.Certificate]
	var renewed tls.Certificate
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		if r.URL.Path == "/healthz" {
			io.WriteString(w, `{"ok":true,"agentVersion":"2026091101","protocol":5}`)
			return
		}
		active.Store(&renewed)
		w.Header().Set("Content-Type", "application/x-ndjson")
		io.WriteString(w, "{\"state\":\"restarting\",\"reason\":\"self_deploy\",\"terminal\":true}\n")
	}))
	srv.StartTLS()
	t.Cleanup(srv.Close)
	old := srv.TLS.Certificates[0]
	leaf := *srv.Certificate()
	leaf.SerialNumber = big.NewInt(1077)
	leaf.SignatureAlgorithm = x509.UnknownSignatureAlgorithm // chosen from the new key
	newKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf.PublicKey = newKey.Public() // self-signed by the new key
	der, err := x509.CreateCertificate(rand.Reader, &leaf, &leaf, newKey.Public(), newKey)
	if err != nil {
		t.Fatal(err)
	}
	renewed = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: newKey}
	keyPin := func(cert tls.Certificate) string {
		pin, err := agent.KeyPin(cert.Certificate[0])
		if err != nil {
			t.Fatal(err)
		}
		return pin
	}
	active.Store(&renewed)
	if rotateOnDeploy {
		active.Store(&old)
	}
	srv.TLS.Certificates = nil
	srv.TLS.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return active.Load(), nil }
	graphPin := keyPin(renewed)
	if !graphMatches {
		graphPin = strings.Repeat("cd", 32)
	}
	graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Error("key pin lookup must be authenticated")
		}
		fmt.Fprintf(w, `{"data":{"device":{"deviceId":"MAC1","publicKeyPin":%q}}}`, graphPin)
	}))
	t.Cleanup(graph.Close)
	t.Setenv("DRIVERFORGE_GRAPH_URL", graph.URL)
	u, _ := url.Parse(srv.URL)
	t.Setenv("DRIVERFORGE_AGENT_PORT", u.Port())
	if err := orgctx.SetCurrent(orgctx.Org{ID: "o1"}); err != nil {
		t.Fatal(err)
	}
	dev := orgctx.Device{ID: "d1", OrgID: "o1", Name: "Controller", DeviceID: "MAC1", IP: u.Hostname(), AgentKeyPin: keyPin(old)}
	if err := orgctx.SetCurrentDevice(dev); err != nil {
		t.Fatal(err)
	}
	return dev, agent.New(srv.URL, dev.AgentKeyPin)
}

func TestVersionRecoversRotatedPin(t *testing.T) {
	dev, _ := pinRecoveryFixture(t, false, true)
	_, rows := controllerRows(context.Background(), dev)
	if rows[0].Value != "2026091101" {
		t.Fatalf("version did not recover: %+v", rows)
	}
}

func TestUpgradeDiscoveryRecoversRotatedPin(t *testing.T) {
	pinRecoveryFixture(t, false, true)
	// No installed filename: discovery stops after health, without a release lookup.
	row := agentRow(context.Background(), io.Discard)
	if row.current != "2026091101" {
		t.Fatalf("upgrade discovery did not recover: %+v", row)
	}
}

func TestSelfUpgradeRecoversRotatedPin(t *testing.T) {
	dev, cl := pinRecoveryFixture(t, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	err := runAgentUpgradePlain(ctx, &out, cl, dev, "agent.c4z", "hash", "", strings.NewReader("driver"), true, "2026082201", "2026091101")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "⚠") {
		t.Fatalf("upgrade failed to confirm after certificate rotation:\n%s", out.String())
	}
	current, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentKeyPin == dev.AgentKeyPin {
		t.Fatal("replacement pin was not persisted")
	}
}

func TestVersionReportsCertificateMismatch(t *testing.T) {
	dev, _ := pinRecoveryFixture(t, false, false)
	_, rows := controllerRows(context.Background(), dev)
	if rows[0].Value != "TLS key mismatch" {
		t.Fatalf("misleading version status: %+v", rows)
	}
}

func TestSelfUpgradeReportsUnresolvedCertificateMismatch(t *testing.T) {
	dev, cl := pinRecoveryFixture(t, true, false)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var out bytes.Buffer
	err := runAgentUpgradePlain(ctx, &out, cl, dev, "agent.c4z", "hash", "", strings.NewReader("driver"), true, "2026082201", "2026091101")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "certificate") || strings.Contains(out.String(), "the agent is restarting — re-check shortly") {
		t.Fatalf("misleading restart status:\n%s", out.String())
	}
}

func TestUpgradeDiscoveryReportsCertificateMismatch(t *testing.T) {
	pinRecoveryFixture(t, false, false)
	row := agentRow(context.Background(), io.Discard)
	if !strings.Contains(row.note, "TLS key mismatch") {
		t.Fatalf("misleading upgrade discovery status: %+v", row)
	}
}

func TestUnverifiedReplacementPinIsNotPersisted(t *testing.T) {
	dev, _ := pinRecoveryFixture(t, false, false)
	_, _, err := dialAgent(context.Background(), dev)
	if err == nil {
		t.Fatal("mismatched certificate accepted")
	}
	current, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentKeyPin != dev.AgentKeyPin {
		t.Fatal("unverified replacement pin persisted")
	}
}

func TestSelfUpgradeTUIRecoversRotatedPin(t *testing.T) {
	dev, cl := pinRecoveryFixture(t, true, true)
	original := newDeployProgram
	t.Cleanup(func() { newDeployProgram = original })
	var model *ui.DeployModel
	newDeployProgram = func(m tea.Model, opts ...tea.ProgramOption) *tea.Program {
		model = m.(*ui.DeployModel)
		opts = append(opts, tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithoutRenderer())
		return tea.NewProgram(m, opts...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := runAgentUpgradeTUI(ctx, cl, dev, "agent.c4z", "hash", "", strings.NewReader("driver"), true, "2026082201", "2026091101")
	if err != nil {
		t.Fatal(err)
	}
	if model.Outcome() != ui.DeployOK || model.TermState() != agent.StateConfirmed || model.Version() != "2026091101" {
		t.Fatalf("upgrade did not confirm in TUI: outcome=%v state=%v version=%s", model.Outcome(), model.TermState(), model.Version())
	}
	current, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentKeyPin == dev.AgentKeyPin {
		t.Fatal("TUI upgrade did not persist replacement pin")
	}
}

func TestPinRecoveryKeepsOriginalClientWhenRefreshUnavailable(t *testing.T) {
	for _, scenario := range []string{"graph error", "empty pin", "missing address"} {
		t.Run(scenario, func(t *testing.T) {
			dev, cl := pinRecoveryFixture(t, false, true)
			if scenario == "missing address" {
				dev.IP, dev.Hostname = "", ""
			} else {
				graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if scenario == "graph error" {
						http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
						return
					}
					io.WriteString(w, `{"data":{"device":{"deviceId":"MAC1","publicKeyPin":null}}}`)
				}))
				t.Cleanup(graph.Close)
				t.Setenv("DRIVERFORGE_GRAPH_URL", graph.URL)
			}
			got, health, err := healthWithPinRecovery(context.Background(), cl, dev)
			if !errors.Is(err, agent.ErrKeyMismatch) {
				t.Fatalf("lost original certificate error: %v", err)
			}
			if got != cl || health.AgentVersion != "" {
				t.Fatal("unverified connection substituted for original client")
			}
			current, err := orgctx.CurrentDevice()
			if err != nil {
				t.Fatal(err)
			}
			if current.AgentKeyPin != dev.AgentKeyPin {
				t.Fatal("cached pin changed without a verified replacement")
			}
		})
	}
}

type recordedDeployProgress struct {
	steps    []string
	logs     []string
	warnings []string
	rows     map[string]string
}

func (p *recordedDeployProgress) Step(s string) { p.steps = append(p.steps, s) }
func (p *recordedDeployProgress) Logf(format string, args ...any) {
	p.logs = append(p.logs, fmt.Sprintf(format, args...))
}
func (p *recordedDeployProgress) Warnf(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}
func (p *recordedDeployProgress) Row(key, value string) { p.rows[key] = value }

func TestFramedDeployRecoversRotatedPin(t *testing.T) {
	dev, cl := pinRecoveryFixture(t, true, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	progress := &recordedDeployProgress{rows: make(map[string]string)}
	terminal, err := deployStreamInFrame(ctx, progress, cl, dev, "agent.c4z", "hash", strings.NewReader("driver"), true, "2026082201", "2026091101")
	if err != nil {
		t.Fatal(err)
	}
	if state := terminal.State; state != agent.StateConfirmed || progress.rows["version"] != "2026091101" || len(progress.warnings) != 0 {
		t.Fatalf("framed deploy did not confirm: state=%s progress=%+v", terminal.State, progress)
	}
	if len(progress.steps) == 0 || len(progress.logs) == 0 {
		t.Fatal("frame received no deploy progress")
	}
	current, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatal(err)
	}
	if current.AgentKeyPin == dev.AgentKeyPin {
		t.Fatal("framed deploy did not persist replacement pin")
	}
}
