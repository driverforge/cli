package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/driverforge/cli/internal/auth"
	"github.com/driverforge/cli/internal/clierr"
)

func TestKeyMismatchIsDetectable(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	_, err := New(srv.URL, strings.Repeat("ab", 32)).Health(context.Background())
	if err == nil {
		t.Fatal("wrong key pin must be refused")
	}
	// The sentinel must survive net/http + TLS wrapping so callers can self-heal.
	if !errors.Is(err, ErrKeyMismatch) {
		t.Errorf("mismatch should be errors.Is ErrKeyMismatch, got %v", err)
	}
	// It must also remain a friendly UserError (never reported as a crash).
	if !clierr.IsUser(err) {
		t.Errorf("mismatch should stay a friendly UserError, got %v", err)
	}
}

func TestKeyPinning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true,"capabilities":["sync"]}`)
	}))
	defer srv.Close()
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo) // the leaf's key
	good := hex.EncodeToString(sum[:])

	if _, err := New(srv.URL, good).Health(context.Background()); err != nil {
		t.Fatalf("matching key pin should connect: %v", err)
	}
	if _, err := New(srv.URL, "sha256:"+strings.ToUpper(good)).Health(context.Background()); err != nil {
		t.Errorf("prefixed/upper key pin should match: %v", err)
	}
	if _, err := New(srv.URL, strings.Repeat("ab", 32)).Health(context.Background()); err == nil {
		t.Error("wrong key pin must be refused")
	}
	// The pin is the key, not the certificate: the whole-certificate hash the
	// CLI used to pin must not match.
	whole := sha256.Sum256(srv.Certificate().Raw)
	if _, err := New(srv.URL, hex.EncodeToString(whole[:])).Health(context.Background()); err == nil {
		t.Error("a certificate fingerprint must not satisfy a key pin")
	}
}

// A certificate reissued for the same key (what renewal does) still matches
// the pin, so a renewal never breaks the connection.
func TestKeyPinSurvivesReissue(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":true}`)
	}))
	srv.StartTLS()
	defer srv.Close()
	pin, err := KeyPin(srv.Certificate().Raw)
	if err != nil {
		t.Fatal(err)
	}

	orig := srv.TLS.Certificates[0]
	tmpl := *srv.Certificate()
	tmpl.SerialNumber = big.NewInt(2)
	tmpl.NotBefore = time.Now().Add(-time.Minute)
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, tmpl.PublicKey, orig.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	reissued := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: orig.PrivateKey}
	srv.TLS.Certificates = nil
	srv.TLS.GetCertificate = func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return &reissued, nil }

	if _, err := New(srv.URL, pin).Health(context.Background()); err != nil {
		t.Errorf("a reissued certificate for the same key should still match: %v", err)
	}
}

func TestNormalizeFingerprint(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	h := hex.EncodeToString(raw)

	var colon strings.Builder
	for i := 0; i < len(h); i += 2 {
		if i > 0 {
			colon.WriteByte(':')
		}
		colon.WriteString(strings.ToUpper(h[i : i+2]))
	}

	for _, in := range []string{h, strings.ToUpper(h), "sha256:" + h, colon.String(), base64.StdEncoding.EncodeToString(raw)} {
		if got := normalizeFingerprint(in); got != h {
			t.Errorf("normalizeFingerprint(%q) = %q, want %q", in, got, h)
		}
	}
}

// stubLogin makes auth.Current return a token without any network call.
func stubLogin(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("DRIVERFORGE_CREDENTIALS_BACKEND", "file")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("DRIVERFORGE_AUTH_URL", "https://auth.example.test")
	if err := auth.Save(auth.Credentials{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("stub login: %v", err)
	}
}

func fakeAgent(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "")
}

func TestHealthAndSupports(t *testing.T) {
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Errorf("path = %s", r.URL.Path)
		}
		io.WriteString(w, `{"ok":true,"agentVersion":"0.1.2","protocol":2,"capabilities":["deploy","sync","driver-state"]}`)
	})
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !h.Supports("sync") || h.Supports("nope") {
		t.Errorf("Supports wrong: %+v", h)
	}
}

func TestSyncSendsHeadersAndBody(t *testing.T) {
	stubLogin(t)
	var gotAuth, gotFile, gotHash, gotBody string
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotFile = r.Header.Get("X-Driver-Filename")
		gotHash = r.Header.Get("X-Driver-Structural-Hash")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"ok":true,"synced":2,"devices":["a","b"],"structuralHash":"sha256:xyz","verified":true}`)
	})
	res, err := c.Sync(context.Background(), "acme-dev.c4z", "sha256:xyz", "", strings.NewReader("LUA"))
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if res.Synced != 2 || res.StructuralHash != "sha256:xyz" || !res.Verified {
		t.Errorf("bad result: %+v", res)
	}
	if gotAuth != "Bearer tok" || gotFile != "acme-dev.c4z" || gotHash != "sha256:xyz" || gotBody != "LUA" {
		t.Errorf("headers/body wrong: auth=%q file=%q hash=%q body=%q", gotAuth, gotFile, gotHash, gotBody)
	}
}

// The agent sends Control4 device IDs as JSON numbers, not strings. Decoding
// must accept either — a numeric id previously failed the whole sync with
// "cannot unmarshal number into Go struct field SyncResult.devices".
func TestSyncAcceptsNumericDeviceIDs(t *testing.T) {
	stubLogin(t)
	for name, body := range map[string]string{
		"numbers": `{"ok":true,"synced":2,"devices":[12,34]}`,
		"strings": `{"ok":true,"synced":2,"devices":["12","34"]}`,
		"mixed":   `{"ok":true,"synced":2,"devices":[12,"34"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
				io.WriteString(w, body)
			})
			res, err := c.Sync(context.Background(), "acme-dev.c4z", "sha256:xyz", "", strings.NewReader("LUA"))
			if err != nil {
				t.Fatalf("Sync: %v", err)
			}
			if got := strings.Join(res.Devices, ","); got != "12,34" {
				t.Errorf("Devices = %q, want \"12,34\"", got)
			}
			if res.Synced != 2 {
				t.Errorf("Synced = %d, want 2", res.Synced)
			}
		})
	}
}

func TestSyncErrorCodes(t *testing.T) {
	stubLogin(t)
	cases := []struct {
		status int
		code   string
	}{
		{http.StatusConflict, CodeNeedsDeploy},
		{http.StatusNotFound, CodeNotInstalled},
		{http.StatusConflict, CodeSelfSyncForbidden},
	}
	for _, tc := range cases {
		c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			io.WriteString(w, `{"ok":false,"error":"`+tc.code+`","message":"nope"}`)
		})
		_, err := c.Sync(context.Background(), "f.c4z", "sha256:1", "", strings.NewReader("x"))
		if Code(err) != tc.code {
			t.Errorf("status %d: Code = %q, want %q", tc.status, Code(err), tc.code)
		}
	}
}

func TestDeployEchoesHash(t *testing.T) {
	stubLogin(t)
	var gotHash string
	c := fakeAgent(t, func(w http.ResponseWriter, r *http.Request) {
		gotHash = r.Header.Get("X-Driver-Structural-Hash")
		io.WriteString(w, `{"ok":true,"filename":"acme-dev.c4z","bytes":10,"reinstall":"scheduled","structuralHash":"sha256:abc"}`)
	})
	res, _, err := c.DeployStream(context.Background(), "acme-dev.c4z", "sha256:abc", "", strings.NewReader("c4zbytes"), false, nil)
	if err != nil {
		t.Fatalf("DeployStream: %v", err)
	}
	if res.StructuralHash != "sha256:abc" || gotHash != "sha256:abc" {
		t.Errorf("deploy hash echo wrong: res=%q sent=%q", res.StructuralHash, gotHash)
	}
}

// A 2xx response whose body says {"ok":false} is a failure, not a success —
// the agent reports the outcome in the body. Sync must surface it as an error,
// not print "synced 0 device(s)".
func TestSyncOkFalseIs2xxFailure(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ok":false,"error":"self_sync_forbidden","message":"can't hot-swap the agent's own driver"}`)
	})
	_, err := c.Sync(context.Background(), "d.c4z", "h", "", strings.NewReader("lua"))
	if err == nil {
		t.Fatal("a 200 {ok:false} must surface as an error, not a fake success")
	}
	if Code(err) != CodeSelfSyncForbidden {
		t.Errorf("error code = %q, want %q", Code(err), CodeSelfSyncForbidden)
	}
}

// A body with no "ok" field (or ok:true) is not treated as a failure.
func TestDoAllowsMissingOrTrueOk(t *testing.T) {
	stubLogin(t)
	for _, body := range []string{`{"agentVersion":"1.0.0","capabilities":[]}`, `{"ok":true,"agentVersion":"1.0.0"}`} {
		c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) })
		if _, err := c.Health(context.Background()); err != nil {
			t.Errorf("body %q should succeed: %v", body, err)
		}
	}
}

// A legacy one-shot deploy response of {"ok":false} must not be synthesized
// into a fake "confirmed" — it's a failure the agent never claimed as success.
func TestDeployStreamLegacyOkFalseFails(t *testing.T) {
	stubLogin(t)
	c := fakeAgent(t, func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"ok":false,"error":"director_busy","message":"director is mid-update"}`)
	})
	_, _, err := c.DeployStream(context.Background(), "x.c4z", "h", "", strings.NewReader("c4z"), false, nil)
	if err == nil {
		t.Fatal("a legacy {ok:false} deploy response must fail, not report confirmed")
	}
	if Code(err) != "director_busy" {
		t.Errorf("error code = %q, want director_busy", Code(err))
	}
}
