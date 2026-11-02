package releaseverify

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// signer stands in for the release signing key: tests hold the private half
// only because they have to produce signatures. In production it never leaves
// the signing service.
type signer struct {
	id      string
	private *rsa.PrivateKey
	public  string
}

func newSigner(t *testing.T, id string) signer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatalf("encoding public key: %v", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return signer{id: id, private: key, public: string(encoded)}
}

// sign produces the sidecar the release pipeline publishes.
func (s signer) sign(t *testing.T, manifest []byte) []byte {
	t.Helper()
	digest := sha256.Sum256(manifest)
	raw, err := rsa.SignPKCS1v15(rand.Reader, s.private, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("signing: %v", err)
	}
	sidecar, err := json.Marshal(signatureDoc{
		Alg:   signingAlg,
		KeyID: s.id,
		Sig:   base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		t.Fatalf("encoding sidecar: %v", err)
	}
	return sidecar
}

// withTrustedKeys swaps the embedded key map for the duration of a test.
func withTrustedKeys(t *testing.T, signers ...signer) {
	t.Helper()
	original := trustedKeys
	replacement := map[string]string{}
	for _, s := range signers {
		replacement[s.id] = s.public
	}
	trustedKeys = replacement
	t.Cleanup(func() { trustedKeys = original })
}

func TestVerifyManifestAcceptsTrustedSignatures(t *testing.T) {
	manifest := []byte(`{"version":"1.2.0","artifacts":[{"name":"driverforge-sdk.zip","sha256":"abc"}]}`)

	t.Run("accepts a signature from a trusted key", func(t *testing.T) {
		s := newSigner(t, "release-v1")
		withTrustedKeys(t, s)

		if err := VerifyManifest("Driverforge SDK", manifest, s.sign(t, manifest)); err != nil {
			t.Fatalf("expected the signature to verify, got: %v", err)
		}
	})

	t.Run("accepts either key during a rotation", func(t *testing.T) {
		retiring := newSigner(t, "release-v1")
		current := newSigner(t, "release-v2")
		withTrustedKeys(t, retiring, current)

		if err := VerifyManifest("Driverforge SDK", manifest, current.sign(t, manifest)); err != nil {
			t.Fatalf("expected the new key to verify: %v", err)
		}
		if err := VerifyManifest("Driverforge SDK", manifest, retiring.sign(t, manifest)); err != nil {
			t.Fatalf("expected the retiring key to still verify: %v", err)
		}
	})
}

func TestVerifyManifestRefusesUntrustedInput(t *testing.T) {
	manifest := []byte(`{"version":"1.2.0","artifacts":[{"name":"driverforge-sdk.zip","sha256":"abc"}]}`)

	t.Run("rejects a signature made by a key we do not hold", func(t *testing.T) {
		ours := newSigner(t, "release-v1")
		// Same key id, different private key — an attacker who knows how our
		// sidecars are shaped but not the signing key.
		attacker := newSigner(t, "release-v1")
		withTrustedKeys(t, ours)

		err := VerifyManifest("Driverforge SDK", manifest, attacker.sign(t, manifest))

		if err == nil {
			t.Fatal("expected a forged signature to be refused")
		}
		if !strings.Contains(err.Error(), "signature is invalid") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects a tampered manifest — the compromised-origin case", func(t *testing.T) {
		s := newSigner(t, "release-v1")
		withTrustedKeys(t, s)
		sidecar := s.sign(t, manifest)

		// What a compromised release store would serve: different bytes, with
		// a sha256 inside them matching its own malicious artifact.
		tampered := []byte(`{"version":"1.2.0","artifacts":[{"name":"driverforge-sdk.zip","sha256":"evil"}]}`)

		if err := VerifyManifest("Driverforge SDK", tampered, sidecar); err == nil {
			t.Fatal("expected a tampered manifest to be refused")
		}
	})

	t.Run("rejects a missing signature rather than passing it through", func(t *testing.T) {
		withTrustedKeys(t, newSigner(t, "release-v1"))

		err := VerifyManifest("Driverforge SDK", manifest, nil)

		if err == nil {
			t.Fatal("expected an unsigned manifest to be refused")
		}
		if !strings.Contains(err.Error(), "not signed") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects a malformed sidecar", func(t *testing.T) {
		withTrustedKeys(t, newSigner(t, "release-v1"))

		if err := VerifyManifest("Driverforge SDK", manifest, []byte("not json")); err == nil {
			t.Fatal("expected a malformed sidecar to be refused")
		}
	})

	t.Run("refuses an algorithm the sidecar names but we do not accept", func(t *testing.T) {
		s := newSigner(t, "release-v1")
		withTrustedKeys(t, s)

		var doc signatureDoc
		if err := json.Unmarshal(s.sign(t, manifest), &doc); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		doc.Alg = "none"
		downgraded, _ := json.Marshal(doc)

		err := VerifyManifest("Driverforge SDK", manifest, downgraded)

		if err == nil {
			t.Fatal("expected an algorithm downgrade to be refused")
		}
		if !strings.Contains(err.Error(), "unsupported algorithm") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestVerifyManifestFailsClosed(t *testing.T) {
	manifest := []byte(`{"version":"1.2.0","artifacts":[{"name":"driverforge-sdk.zip","sha256":"abc"}]}`)

	t.Run("fails closed when the build embeds no keys", func(t *testing.T) {
		s := newSigner(t, "release-v1")
		withTrustedKeys(t)

		err := VerifyManifest("Driverforge SDK", manifest, s.sign(t, manifest))

		if err == nil {
			t.Fatal("expected a build with no embedded keys to refuse everything")
		}
		if !strings.Contains(err.Error(), "no release signing keys") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestVerifyManifestErrorsTellTheUserWhatToDo(t *testing.T) {
	manifest := []byte(`{"version":"1.2.0","artifacts":[{"name":"driverforge-sdk.zip","sha256":"abc"}]}`)

	t.Run("tells an out-of-date build apart from a forgery", func(t *testing.T) {
		// A CLI predating the rotation meets a release signed by the new key.
		future := newSigner(t, "release-v2")
		withTrustedKeys(t, newSigner(t, "release-v1"))

		err := VerifyManifest("Driverforge SDK", manifest, future.sign(t, manifest))

		if err == nil {
			t.Fatal("expected an unknown key id to be refused")
		}
		if !strings.Contains(err.Error(), "upgrade the CLI") {
			t.Fatalf("an unknown key id should tell the user to upgrade, got: %v", err)
		}
	})

	t.Run("reports a malformed embedded key distinctly from a bad signature", func(t *testing.T) {
		s := newSigner(t, "release-v1")
		withTrustedKeys(t, s)
		trustedKeys["release-v1"] = "not-a-pem-key"

		err := VerifyManifest("Driverforge SDK", manifest, s.sign(t, manifest))

		if err == nil {
			t.Fatal("expected a malformed embedded key to error")
		}
		if !strings.Contains(err.Error(), "malformed") {
			t.Fatalf("a malformed embedded key is a build fault and must say so, got: %v", err)
		}
	})

	t.Run("names the artifact so the message says what refused to install", func(t *testing.T) {
		withTrustedKeys(t)

		err := VerifyManifest("Anvil Agent", manifest, []byte("{}"))

		if err == nil || !strings.Contains(err.Error(), "Anvil Agent") {
			t.Fatalf("expected the artifact name in the error, got: %v", err)
		}
	})
}

func TestFetchSignature(t *testing.T) {
	t.Run("returns the sidecar bytes", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, ".sig") {
				t.Errorf("expected the .sig sibling, got %s", r.URL.Path)
			}
			w.Write([]byte(`{"alg":"RS256"}`))
		}))
		defer srv.Close()

		got, err := FetchSignature(context.Background(), srv.Client(), srv.URL+"/manifest.json")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(got) != `{"alg":"RS256"}` {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("treats a missing signature as unsigned, not a transport error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		got, err := FetchSignature(context.Background(), srv.Client(), srv.URL+"/manifest.json")

		// Stripping the signature is what an attacker would do; the refusal
		// must come from VerifyManifest, not read as a network problem.
		if err != nil {
			t.Fatalf("a 404 must not be an error, got: %v", err)
		}
		if got != nil {
			t.Fatalf("expected no sidecar bytes, got %q", got)
		}
		if err := VerifyManifest("Driverforge SDK", []byte("{}"), got); err == nil {
			t.Fatal("expected the empty sidecar to be refused downstream")
		}
	})

	t.Run("surfaces other HTTP failures", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		if _, err := FetchSignature(context.Background(), srv.Client(), srv.URL+"/manifest.json"); err == nil {
			t.Fatal("expected a 500 to error")
		}
	})

	t.Run("caps an unbounded response body", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for i := 0; i < 200; i++ {
				w.Write(make([]byte, 1<<10))
			}
		}))
		defer srv.Close()

		got, err := FetchSignature(context.Background(), srv.Client(), srv.URL+"/manifest.json")

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) > 64<<10 {
			t.Fatalf("expected the read to be capped, got %d bytes", len(got))
		}
	})

	t.Run("SignatureURL appends .sig", func(t *testing.T) {
		if got := SignatureURL("https://x/v1/manifest.json"); got != "https://x/v1/manifest.json.sig" {
			t.Fatalf("got %q", got)
		}
	})
}

// TestEmbeddedKeys guards the real trustedKeys map rather than a fixture. A
// mangled paste would otherwise only surface when a user tried to install.
func TestEmbeddedKeys(t *testing.T) {
	t.Run("the build embeds at least one key", func(t *testing.T) {
		if len(trustedKeys) == 0 {
			t.Fatal("no release signing keys embedded — every install would fail closed")
		}
	})

	t.Run("every embedded key parses as an RSA public key", func(t *testing.T) {
		for id, encoded := range trustedKeys {
			key, err := parsePublicKey(encoded)
			if err != nil {
				t.Errorf("embedded key %q does not parse: %v", id, err)
				continue
			}
			if size := key.N.BitLen(); size < 2048 {
				t.Errorf("embedded key %q is %d bits, want >= 2048", id, size)
			}
		}
	})
}
