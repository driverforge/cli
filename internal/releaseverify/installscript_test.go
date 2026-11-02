package releaseverify

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// scripts/install.sh is the bootstrap path: it runs before any verified code
// exists on the machine, so it re-implements this package's trust chain in POSIX
// shell rather than calling into it. These tests hold that second implementation
// to the same contract — the key it embeds must be the key this package trusts,
// and every way the chain can be broken must abort the install.

const installScript = "../../scripts/install.sh"

// fixtureKeyID is the key the chain fixtures sign as: the one current releases
// are signed with, so the script's path for it is the one under test.
const fixtureKeyID = "release-signing-v2"

// keysInScript pulls each embedded key out of the shell script's
// KEY_ID_VN="…" / PUBKEY_VN='…' pair, keyed by the id.
var keysInScript = regexp.MustCompile(`(?s)KEY_ID_(V\d+)="([^"]+)"\s+PUBKEY_(V\d+)='(-----BEGIN PUBLIC KEY-----.*?-----END PUBLIC KEY-----)'`)

// The installer carries its own copy of the signing keys because it cannot
// import this package. Two copies of a trust root is a drift hazard, so pin them
// together: rotating a key here without updating the script would leave every
// curl|sh install trusting a retired key, or refusing the current one.
func TestInstallScriptEmbedsTheSameKeys(t *testing.T) {
	src, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("reading %s: %v", installScript, err)
	}

	inScript := map[string]string{}
	for _, m := range keysInScript.FindAllSubmatch(src, -1) {
		if string(m[1]) != string(m[3]) {
			t.Errorf("install script pairs KEY_ID_%s with PUBKEY_%s", m[1], m[3])
		}
		inScript[string(m[2])] = normalisePEM(string(m[4]))
	}

	for id, want := range trustedKeys {
		got, ok := inScript[id]
		if !ok {
			t.Errorf("install script does not embed %q, which trustedKeys trusts", id)
			continue
		}
		if got != normalisePEM(want) {
			t.Errorf("install script's embedded key does not match trustedKeys[%s]\nscript:\n%s\n\npackage:\n%s", id, got, normalisePEM(want))
		}
	}
	for id := range inScript {
		if _, ok := trustedKeys[id]; !ok {
			t.Errorf("install script embeds %q, which trustedKeys does not trust", id)
		}
	}
}

// Every embedded key must parse as the RSA key the verifier expects; a key that
// is pasted in wrong would otherwise only show up when a release is installed.
func TestTrustedKeysParse(t *testing.T) {
	for id, encoded := range trustedKeys {
		if _, err := parsePublicKey(encoded); err != nil {
			t.Errorf("trustedKeys[%s]: %v", id, err)
		}
	}
}

// The full chain, exercised by running the real script against a release store
// that looks like production: an unmodified fixture installs, and each way of
// breaking the chain refuses to.
func TestInstallScriptVerifiesTheReleaseChain(t *testing.T) {
	requireShellTools(t)

	for _, c := range []struct {
		name string
		// break mutates the served fixture; nil leaves it intact.
		breakIt func(*releaseFixture)
		wantErr string // substring the failure must explain
		pin     string // DRIVERFORGE_VERSION, if any
	}{
		{name: "unmodified release installs"},
		{
			name:    "tampered archive",
			breakIt: func(f *releaseFixture) { f.archive = append(f.archive, "evil"...) },
			wantErr: "checksum mismatch",
		},
		{
			name: "tampered manifest",
			breakIt: func(f *releaseFixture) {
				f.manifest = bytes.Replace(f.manifest, []byte(`"version": "9.9.9"`), []byte(`"version": "9.9.8"`), 1)
			},
			wantErr: "signature is invalid",
		},
		{
			name:    "manifest not signed",
			breakIt: func(f *releaseFixture) { f.sidecar = nil },
			wantErr: "not signed",
		},
		{
			name: "signed with an unknown key",
			breakIt: func(f *releaseFixture) {
				f.sidecar = bytes.Replace(f.sidecar, []byte(`"`+fixtureKeyID+`"`), []byte(`"release-signing-v9"`), 1)
			},
			wantErr: "unknown key",
		},
		{
			name: "unexpected algorithm",
			breakIt: func(f *releaseFixture) {
				f.sidecar = bytes.Replace(f.sidecar, []byte(`"RS256"`), []byte(`"none"`), 1)
			},
			wantErr: "unexpected signature algorithm",
		},
		{
			name:    "pinned version that was never released",
			pin:     "1.2.3",
			wantErr: "could not fetch the release manifest",
		},
		{
			// A stale or redirected document served under the pinned version's
			// path: correctly signed, but describing a different release. Without
			// the cross-check the installer would silently install that instead.
			name:    "pinned version the verified manifest does not describe",
			pin:     "1.2.3",
			breakIt: func(f *releaseFixture) { f.extraManifestDirs = []string{"v1.2.3"} },
			wantErr: "asked for 1.2.3",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			fix := newReleaseFixture(t)
			if c.breakIt != nil {
				c.breakIt(fix)
			}
			srv := fix.serve(t)
			defer srv.Close()

			dir := t.TempDir()
			out, err := runInstaller(t, fix, srv.URL, dir, c.pin)

			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("install failed: %v\n%s", err, out)
				}
				installed := filepath.Join(dir, "driverforge")
				if _, statErr := os.Stat(installed); statErr != nil {
					t.Fatalf("driverforge was not installed: %v\n%s", statErr, out)
				}
				return
			}

			if err == nil {
				t.Fatalf("install succeeded but should have refused (%s)\n%s", c.name, out)
			}
			if !strings.Contains(out, c.wantErr) {
				t.Errorf("failure did not explain itself: want %q in output, got:\n%s", c.wantErr, out)
			}
			// Refusing must mean nothing was installed, not a partial write.
			if _, statErr := os.Stat(filepath.Join(dir, "driverforge")); statErr == nil {
				t.Error("refused the release but installed the binary anyway")
			}
		})
	}
}

// releaseFixture is a complete, signed release: the archive, the manifest naming
// its digest, and the detached signature over that manifest.
type releaseFixture struct {
	version  string
	asset    string
	archive  []byte
	manifest []byte
	sidecar  []byte
	pubPEM   string
	// extraManifestDirs serves the manifest under additional version directories,
	// standing in for a stale or redirected document.
	extraManifestDirs []string
}

func newReleaseFixture(t *testing.T) *releaseFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating test key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("encoding test key: %v", err)
	}

	const version = "9.9.9"
	asset := fmt.Sprintf("driverforge_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	archive := tarGzBinary(t)
	sum := sha256.Sum256(archive)

	// The SBOM entry is listed FIRST, and its name is the archive's name plus a
	// suffix. A lookup that matched on prefix would take this digest, fail the
	// checksum and refuse the install — so the happy-path case below is what
	// proves the archive and its SBOM stay distinguishable in the manifest.
	manifest := []byte(fmt.Sprintf(`{
  "version": "%s",
  "tag": "v%s",
  "artifacts": [
    {
      "name": "%s.sbom.json",
      "url": "https://example.test/%s.sbom.json",
      "sha256": "%s"
    },
    {
      "name": "%s",
      "url": "https://example.test/%s",
      "sha256": "%s"
    }
  ]
}
`, version, version, asset, asset, strings.Repeat("b", 64), asset, asset, hex.EncodeToString(sum[:])))

	return &releaseFixture{
		version:  version,
		asset:    asset,
		archive:  archive,
		manifest: manifest,
		sidecar:  signManifest(t, key, manifest),
		pubPEM:   string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}
}

// signManifest produces a sidecar in the exact shape the release workflow does.
func signManifest(t *testing.T, key *rsa.PrivateKey, manifest []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(manifest)
	raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing fixture manifest: %v", err)
	}
	sidecar, err := json.Marshal(signatureDoc{
		Alg:   signingAlg,
		KeyID: fixtureKeyID,
		Sig:   base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		t.Fatalf("encoding fixture sidecar: %v", err)
	}
	return sidecar
}

// serve stands up a release store with the same layout as the real one. The
// signature 404s when the fixture has none, mirroring an unsigned release.
func (f *releaseFixture) serve(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	serve := func(path string, body []byte) {
		mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
			if body == nil {
				http.NotFound(w, &http.Request{})
				return
			}
			_, _ = w.Write(body)
		})
	}
	for _, dir := range append([]string{"latest", "v" + f.version}, f.extraManifestDirs...) {
		serve("/driverforge-cli/"+dir+"/manifest.json", f.manifest)
		serve("/driverforge-cli/"+dir+"/manifest.json.sig", f.sidecar)
	}
	serve("/driverforge-cli/v"+f.version+"/"+f.asset, f.archive)
	return httptest.NewServer(mux)
}

// runInstaller rewrites the script's two build-time constants — the release store
// it reads and the key it trusts — then runs it. Everything else, including the
// whole verification path under test, is the shipped script unmodified.
func runInstaller(t *testing.T, f *releaseFixture, baseURL, installDir, pin string) (string, error) {
	t.Helper()

	src, err := os.ReadFile(installScript)
	if err != nil {
		t.Fatalf("reading %s: %v", installScript, err)
	}
	script := string(src)

	const realBase = `ARTIFACT_BASE="https://releases.driverforge.com/driverforge-releases/driverforge-cli"`
	testBase := fmt.Sprintf("ARTIFACT_BASE=%q", baseURL+"/driverforge-cli")
	if !strings.Contains(script, realBase) {
		t.Fatal("install script's ARTIFACT_BASE assignment has changed — update this test")
	}
	script = strings.Replace(script, realBase, testBase, 1)

	var embedded string
	for _, m := range keysInScript.FindAllStringSubmatch(script, -1) {
		if m[2] == fixtureKeyID {
			embedded = m[4]
		}
	}
	if embedded == "" {
		t.Fatalf("no %s key in the install script", fixtureKeyID)
	}
	script = strings.Replace(script, embedded, strings.TrimSpace(f.pubPEM), 1)

	path := filepath.Join(t.TempDir(), "install.sh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("writing patched script: %v", err)
	}

	cmd := exec.Command("sh", path)
	cmd.Env = append(os.Environ(), "DRIVERFORGE_INSTALL_DIR="+installDir)
	if pin != "" {
		cmd.Env = append(cmd.Env, "DRIVERFORGE_VERSION="+pin)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// tarGzBinary builds a .tar.gz holding an executable named driverforge, which is
// all the installer extracts.
func tarGzBinary(t *testing.T) []byte {
	t.Helper()
	body := []byte("#!/bin/sh\necho fixture\n")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "driverforge", Mode: 0o755, Size: int64(len(body))}); err != nil {
		t.Fatalf("tar header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("tar body: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("closing tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("closing gzip: %v", err)
	}
	return buf.Bytes()
}

func requireShellTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is the POSIX install path; Windows uses Scoop")
	}
	for _, bin := range []string{"sh", "openssl", "tar", "curl"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not available", bin)
		}
	}
}

func normalisePEM(s string) string { return strings.Join(strings.Fields(s), "") }
