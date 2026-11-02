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
	"fmt"
	"io"
	"net/http"
)

// signingAlg is the only algorithm this build accepts. Pinning it here means a
// sidecar cannot talk us into a weaker scheme by naming one.
const signingAlg = "RS256"

// trustedKeys maps a key id to its PEM-encoded RSA public key.
//
// A map rather than a single key so rotation needs no flag day: the pipeline
// signs with the new key while builds already installed still trust the old
// one, and the old entry is dropped once those builds have aged out. The CLI
// has no self-updater, so a single-key design would strand every installed
// copy the moment a key was retired.
//
// Releases are signed by the release pipeline through a cloud signing service,
// which holds the private half; no build machine or CI secret store ever has
// it, so there is no private key to embed, leak, or rotate out of one. An
// empty map fails closed; see VerifyManifest.
var trustedKeys = map[string]string{
	// Public half of `release-signing-v1`, which signed releases up to v0.8.5.
	// It signs nothing new; it stays so those releases still verify. There is
	// nothing secret here — this is exactly the value we want reviewable in
	// source, since it is a trust root for the releases this build installs.
	"release-signing-v1": `-----BEGIN PUBLIC KEY-----
MIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtqHcm7GC4GjkU3Or50E8
NY7IqTnIQdM6Rwubu1URuGD2gLUx3gPFb7JLvXM/X4hDe8PZvVPxmFb9spdHdL2h
iWAqvC+XcjibcRHV+McAhS4VaE5G7RaATXBJOnQBvTg7ztZ/MzYO02FDu1pJsE3o
f9ycCCXX8EsOwDdJ9ecaZb7TElqyIrWkM5dvbJSaldb+YXB7KjpPgwwCnsW8lF+G
WRSsCcuFkEUs9ZQQxnP6QWRyHIByZ6qM8l/zDOCKZD70rCz0jKBr7oRVkjvxJ2nG
ZTf7bSPrDK6uCdGH3EwRoEZJemHnAvjv1HwZRKdOTAElsU2AEKpzf5lnWXRMS+3V
OQIDAQAB
-----END PUBLIC KEY-----`,
	// Public half of `release-signing-v2` (RSA-4096), which signs every
	// release after CLI v0.8.5: the CLI's, the SDK's and the agent's.
	// SHA-256 of its SPKI DER encoding:
	// 7b286fbc3d225d1ff5d97d0ecb42f1ef81430c0d47f99ced10c79c4ab8af0939
	"release-signing-v2": `-----BEGIN PUBLIC KEY-----
MIICIjANBgkqhkiG9w0BAQEFAAOCAg8AMIICCgKCAgEAyZE6JXIc9y04KJc4y9no
Tst3j//GKRAkvEkrtB0rCq7Nj8bu94O+xQSizFxtYUW9PeI3TaiHLUBtcbaiBglF
hG6Q2mfPZuvFWdwE1I1+Sun7bU+xz27IQkSNiuVSLYhdXojFyPj4o21U2RFGUv6j
n6tyi/0aZvuzmKlwxb8ooNNQCLK8y4aagZmKbYGCVEtACFweuMnppDAzzet+nVgZ
bqvRbGO5LF5UKtUum0N9tG1EKNVUtywAGzyUI9rv6WkPo5d9y3DMS/6RnP/eNojR
DTCNFE6E8t7A/pmXvY+OMhiqhfb5/6S+0ILW0p2kzomIRU5whD9dsW5uUWGuw/sF
gXobjPPVyUrNceUNZZLEFW1VZPzXy2inVP6gbSsWB/VgQISD/ZOB8yqjAAYvzeqn
p/+iaRc+HXlC5Tscukn/NfYbnfitGSq0bmWPqofX1PwaXt/lHPgwiuH+PXqHIWVg
Aj8471t0/gjO8NXXsPITFdJJ4DVgyj61gtwEUT78XEn7cDag+Ix00KxKW19kwuTl
rRfNl6vOIeCzuSPaCaMjJJuY16L3IrFigtNmYzf5k5AALextXLWHbt4NAzr9MLU7
UFwiEUs/FECtpeTqksv0wMXYfFnFWLLLUnPeW5gO7mXlNqVyV3b/+ptQrDgShE6i
/a+jFZUfYYHGYhaFmcEX8hMCAwEAAQ==
-----END PUBLIC KEY-----`,
}

// signatureDoc is the detached sidecar published next to a manifest. It is
// self-describing so rotation can select a key by id rather than trial-
// verifying every key we know, which lets us tell "signed by a key this build
// is too old to know about" apart from "not signed by us at all".
type signatureDoc struct {
	Alg   string `json:"alg"`
	KeyID string `json:"keyId"`
	Sig   string `json:"sig"`
}

// VerifyManifest checks a detached signature over the exact manifest bytes.
//
// This is the part Verify cannot do. Verify compares an artifact against a
// checksum that arrived from the same origin as the artifact, so a compromised
// release store can serve malicious bytes and a matching sha256. Trust here
// rests on a key compiled into this binary, whose private half lives only
// inside the signing service. The manifest carries the artifact's sha256, so a valid
// signature over the manifest plus a matching checksum chains the artifact
// back to the embedded key.
//
// manifest must be the bytes as served, not a re-encoding of the parsed
// document — JSON round-tripping reorders keys and breaks the signature.
func VerifyManifest(artifact string, manifest, sidecar []byte) error {
	if len(trustedKeys) == 0 {
		return fmt.Errorf("%s: this build has no release signing keys embedded — refusing to install unverified code", artifact)
	}
	if len(sidecar) == 0 {
		return fmt.Errorf("%s: release manifest is not signed — refusing to install unverified code", artifact)
	}

	var doc signatureDoc
	if err := json.Unmarshal(sidecar, &doc); err != nil {
		return fmt.Errorf("%s: release signature is malformed — refusing to install: %w", artifact, err)
	}
	if doc.Alg != signingAlg {
		return fmt.Errorf("%s: release signature uses unsupported algorithm %q — refusing to install", artifact, doc.Alg)
	}

	encodedKey, known := trustedKeys[doc.KeyID]
	if !known {
		return fmt.Errorf("%s: release is signed by key %q, which this build does not trust — upgrade the CLI", artifact, doc.KeyID)
	}

	key, err := parsePublicKey(encodedKey)
	if err != nil {
		// Our own build artifact is malformed. Fail loudly rather than
		// letting it read as an untrusted signature.
		return fmt.Errorf("%s: embedded release signing key %q is malformed: %w", artifact, doc.KeyID, err)
	}

	signature, err := base64.StdEncoding.DecodeString(doc.Sig)
	if err != nil {
		return fmt.Errorf("%s: release signature is not valid base64 — refusing to install", artifact)
	}

	digest := sha256.Sum256(manifest)
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return fmt.Errorf("%s: release manifest signature is invalid — refusing to install", artifact)
	}
	return nil
}

func parsePublicKey(encoded string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(encoded))
	if block == nil {
		return nil, fmt.Errorf("not PEM-encoded")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("not an RSA public key (%T)", parsed)
	}
	return key, nil
}

// SignatureURL is where the detached signature for a manifest lives: the
// manifest URL with .sig appended.
func SignatureURL(manifestURL string) string { return manifestURL + ".sig" }

// FetchSignature retrieves the detached signature for a manifest.
//
// A 404 is returned as an empty signature rather than an error, so the refusal
// comes from VerifyManifest with a message about unsigned releases instead of
// an HTTP status the user cannot act on. Stripping the signature file is
// exactly what an attacker would try, and it must not read as a transport
// failure.
func FetchSignature(ctx context.Context, client *http.Client, manifestURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, SignatureURL(manifestURL), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching release signature: HTTP %d", resp.StatusCode)
	}
	// Sidecars are a few hundred bytes; the cap stops a hostile origin
	// streaming us something unbounded.
	return io.ReadAll(io.LimitReader(resp.Body, 64<<10))
}

// SigningFixtureForTesting installs a freshly generated trusted key and returns
// a signer that produces sidecars the way the release pipeline does, plus a
// function restoring the previous key set.
//
// Test-only. It exists so packages that fetch manifests can serve signed
// fixtures without each reimplementing the sidecar format, and because the
// production key's private half lives in the signing service and cannot sign in a test.
// This package is under internal/, so the seam cannot escape the module.
func SigningFixtureForTesting(keyID string) (sign func(manifest []byte) []byte, restore func()) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic("releaseverify: generating test key: " + err.Error())
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		panic("releaseverify: encoding test key: " + err.Error())
	}

	previous := trustedKeys
	trustedKeys = map[string]string{
		keyID: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
	}

	sign = func(manifest []byte) []byte {
		digest := sha256.Sum256(manifest)
		raw, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			panic("releaseverify: signing test manifest: " + err.Error())
		}
		sidecar, err := json.Marshal(signatureDoc{
			Alg:   signingAlg,
			KeyID: keyID,
			Sig:   base64.StdEncoding.EncodeToString(raw),
		})
		if err != nil {
			panic("releaseverify: encoding test sidecar: " + err.Error())
		}
		return sidecar
	}
	return sign, func() { trustedKeys = previous }
}

// ServeSignedManifestForTesting registers a manifest and its detached
// signature on mux, so a test server looks like the real release store.
// Test-only, and paired with SigningFixtureForTesting.
func ServeSignedManifestForTesting(mux *http.ServeMux, path string, manifest []byte, sign func([]byte) []byte) {
	mux.HandleFunc(path, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(manifest)
	})
	mux.HandleFunc(path+".sig", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(sign(manifest))
	})
}
