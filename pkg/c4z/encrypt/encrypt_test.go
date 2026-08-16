package encrypt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/smallstep/pkcs7"
)

// TestDefaultParsesEmbeddedCert ensures the embedded DriverWorks certificate
// decodes and Default() returns a usable encryptor whose output is a parseable
// PKCS#7 envelope to that recipient.
func TestDefaultParsesEmbeddedCert(t *testing.T) {
	enc := Default()
	der, err := enc.Encrypt([]byte("local x = 1\n"))
	if err != nil {
		t.Fatalf("Default().Encrypt: %v", err)
	}
	if _, err := pkcs7.Parse(der); err != nil {
		t.Fatalf("output is not a parseable PKCS#7 structure: %v", err)
	}
}

// TestEncryptRoundTrip proves the envelope produced for a recipient is valid and
// decryptable with that recipient's private key (the authoritative structural
// check we can run without Control4 hardware), exercising AES-256-CBC content
// encryption with RSA PKCS#1 v1.5 key transport.
func TestEncryptRoundTrip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-recipient"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31-1, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("package.preload['x'] = (function (...) return 42 end)\n")
	enc := &driverWorks{cert: cert}
	envelope, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}

	p7, err := pkcs7.Parse(envelope)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got, err := p7.Decrypt(cert, key)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round-trip mismatch:\n got %q\nwant %q", got, plaintext)
	}
}
