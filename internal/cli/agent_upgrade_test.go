package cli

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/driverforge/cli/internal/clierr"
	"github.com/driverforge/cli/internal/orgctx"
	"github.com/driverforge/cli/internal/releaseverify"
)

// devAgentC4z is the fake installed agent filename the upgrade tests key the
// release artifact and deploy headers on.
const devAgentC4z = "anvil-agent-dev.c4z"

// mustCurrentDevice returns the device setupSyncEnv put in context.
func mustCurrentDevice(t *testing.T) orgctx.Device {
	t.Helper()
	dev, err := orgctx.CurrentDevice()
	if err != nil {
		t.Fatalf("no device in context: %v", err)
	}
	return dev
}

// fakeReleaseStore serves the store's shared manifest schema + the artifact
// zip for the agent, keyed under DRIVERFORGE_RELEASES_BASE_URL. The zip wraps
// anvil-agent-dev.c4z, mirroring the real release layout.
func fakeReleaseStore(t *testing.T, version string, zipBlob []byte, zipSHA string) {
	t.Helper()
	sign, restore := releaseverify.SigningFixtureForTesting("test-key")
	t.Cleanup(restore)

	// Built once and served verbatim so the signature covers the bytes the
	// client actually reads; assigned after the server exists because the
	// artifact URL embeds srv.URL.
	var manifest []byte
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "manifest.json.sig"):
			w.Write(sign(manifest))
		case strings.HasSuffix(r.URL.Path, "manifest.json"):
			w.Write(manifest)
		default:
			w.Write(zipBlob)
		}
	}))
	manifest = []byte(fmt.Sprintf(`{"version":%q,"artifacts":[{"name":"anvil-agent-dev-%s.zip","url":%q,"sha256":%q,"contents":[%q]}]}`,
		version, version, srv.URL+"/anvil-agent-dev/"+version+"/anvil-agent-dev.zip", zipSHA, devAgentC4z))
	t.Cleanup(srv.Close)
	t.Setenv("DRIVERFORGE_RELEASES_BASE_URL", srv.URL)
}

// fakeUpgradeAgent serves /healthz (version flips to newVer once /deploy is hit)
// and a streaming self-deploy /deploy. It records the deploy headers.
type fakeUpgradeAgent struct {
	mu          sync.Mutex
	curVer      string
	newVer      string
	filename    string // "" omits agentFilename (older agent)
	noChecksum  bool   // omit the checksum capability (older agent)
	deployed    bool
	gotFilename string
	gotSHA      string
	deployHits  int
}

func (f *fakeUpgradeAgent) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			f.mu.Lock()
			ver := f.curVer
			if f.deployed {
				ver = f.newVer
			}
			fn := f.filename
			noSum := f.noChecksum
			f.mu.Unlock()
			fnField := ""
			if fn != "" {
				fnField = fmt.Sprintf(`"agentFilename":%q,`, fn)
			}
			caps := `["deploy","deploy-stream","checksum"]`
			if noSum {
				caps = `["deploy","deploy-stream"]`
			}
			fmt.Fprintf(w, `{"ok":true,"agentVersion":%q,%s"protocol":4,"capabilities":%s}`, ver, fnField, caps)
		case "/deploy":
			f.mu.Lock()
			f.deployHits++
			f.gotFilename = r.Header.Get("X-Driver-Filename")
			f.gotSHA = r.Header.Get("X-Driver-SHA256")
			f.deployed = true
			f.mu.Unlock()
			io.WriteString(w, `{"state":"restarting","reason":"self_deploy","step":"agent restarting","terminal":true}`+"\n")
		}
	}
}

// newUpgradeBlob builds the release zip wrapping the fake agent .c4z, returning
// the zip, its checksum (what the manifest carries), and the payload's checksum
// (what the CLI must forward to the agent as X-Driver-SHA256).
func newUpgradeBlob(t *testing.T) (zipBlob []byte, zipSHA, payloadSHA string) {
	t.Helper()
	payload := "DRIVERFORGE-AGENT-C4Z-PAYLOAD"
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(devAgentC4z)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	zipSum := sha256.Sum256(buf.Bytes())
	paySum := sha256.Sum256([]byte(payload))
	return buf.Bytes(), hex.EncodeToString(zipSum[:]), hex.EncodeToString(paySum[:])
}

func TestRunAgentUpgradeHappyPath(t *testing.T) {
	zipBlob, zipSHA, payloadSHA := newUpgradeBlob(t)
	fa := &fakeUpgradeAgent{curVer: "0.1.2", newVer: "0.1.5", filename: devAgentC4z}
	srv := httptest.NewTLSServer(fa.handler())
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)
	fakeReleaseStore(t, "0.1.5", zipBlob, zipSHA)

	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	var out bytes.Buffer
	if err := runAgentUpgrade(context.Background(), &out, mustCurrentDevice(t)); err != nil {
		t.Fatalf("runAgentUpgrade: %v", err)
	}

	fa.mu.Lock()
	defer fa.mu.Unlock()
	// Note 1: deployed under the EXACT installed filename → self-deploy engages.
	if fa.gotFilename != devAgentC4z {
		t.Errorf("deploy X-Driver-Filename = %q, want anvil-agent-dev.c4z", fa.gotFilename)
	}
	// Note 6: checksum of the EXTRACTED .c4z (the uploaded bytes) sent for
	// CLI→agent integrity — not the zip's.
	if fa.gotSHA != payloadSHA {
		t.Errorf("X-Driver-SHA256 = %q, want the payload checksum %q", fa.gotSHA, payloadSHA)
	}
	// Note 7: confirmed via the /healthz version flip after restart.
	if !strings.Contains(out.String(), "0.1.5") {
		t.Errorf("output should report the upgraded version:\n%s", out.String())
	}
}

// An agent that doesn't advertise the checksum capability fails CLOSED on the
// X-Driver-SHA256 header ("SHA256 not available on this controller") — the
// CLI must not send it, so the upgrade still goes through. The release bytes
// were already verified against the store manifest.
func TestRunAgentUpgradeOmitsChecksumWithoutCapability(t *testing.T) {
	zipBlob, zipSHA, _ := newUpgradeBlob(t)
	fa := &fakeUpgradeAgent{curVer: "0.1.2", newVer: "0.1.5", filename: devAgentC4z, noChecksum: true}
	srv := httptest.NewTLSServer(fa.handler())
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)
	fakeReleaseStore(t, "0.1.5", zipBlob, zipSHA)

	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	var out bytes.Buffer
	if err := runAgentUpgrade(context.Background(), &out, mustCurrentDevice(t)); err != nil {
		t.Fatalf("runAgentUpgrade: %v", err)
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.deployHits != 1 {
		t.Fatalf("deploy hits = %d, want 1", fa.deployHits)
	}
	if fa.gotSHA != "" {
		t.Errorf("X-Driver-SHA256 = %q — must be omitted for an agent without the checksum capability", fa.gotSHA)
	}
}

func TestRunAgentUpgradeAlreadyCurrent(t *testing.T) {
	zipBlob, zipSHA, _ := newUpgradeBlob(t)
	fa := &fakeUpgradeAgent{curVer: "0.1.5", newVer: "0.1.5", filename: devAgentC4z}
	srv := httptest.NewTLSServer(fa.handler())
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)
	fakeReleaseStore(t, "0.1.5", zipBlob, zipSHA)

	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	var out bytes.Buffer
	if err := runAgentUpgrade(context.Background(), &out, mustCurrentDevice(t)); err != nil {
		t.Fatalf("runAgentUpgrade: %v", err)
	}
	if !strings.Contains(out.String(), "up to date") {
		t.Errorf("should report up to date:\n%s", out.String())
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.deployHits != 0 {
		t.Errorf("an up-to-date agent must not be deployed (hits=%d)", fa.deployHits)
	}
}

func TestRunAgentUpgradeTooOldToSelfUpgrade(t *testing.T) {
	fa := &fakeUpgradeAgent{curVer: "0.1.2", newVer: "0.1.5", filename: ""} // no agentFilename
	srv := httptest.NewTLSServer(fa.handler())
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)

	flagNoTUI = true
	t.Cleanup(func() { flagNoTUI = false })

	err := runAgentUpgrade(context.Background(), &bytes.Buffer{}, mustCurrentDevice(t))
	if !clierr.IsUser(err) {
		t.Fatalf("a too-old agent should be a friendly UserError, got %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "Composer") {
		t.Errorf("should point at Composer Pro: %q", err.Error())
	}
}

func TestRunUpgradeAggregatorReportsAgentRow(t *testing.T) {
	zipBlob, zipSHA, _ := newUpgradeBlob(t)
	fa := &fakeUpgradeAgent{curVer: "0.1.2", newVer: "0.1.5", filename: devAgentC4z}
	srv := httptest.NewTLSServer(fa.handler())
	t.Cleanup(srv.Close)
	setupSyncEnv(t, srv)
	fakeReleaseStore(t, "0.1.5", zipBlob, zipSHA)

	flagNoTUI = true // non-interactive aggregator → report only, no mutation
	t.Cleanup(func() { flagNoTUI = false })

	var out bytes.Buffer
	if err := runUpgrade(context.Background(), &out); err != nil {
		t.Fatalf("runUpgrade: %v", err)
	}
	s := out.String()
	for _, want := range []string{"Anvil Agent", "0.1.5", "update available", "driverforge upgrade"} {
		if !strings.Contains(s, want) {
			t.Errorf("aggregator output missing %q:\n%s", want, s)
		}
	}
	fa.mu.Lock()
	defer fa.mu.Unlock()
	if fa.deployHits != 0 {
		t.Error("non-interactive aggregator must not mutate")
	}
}
