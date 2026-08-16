// Package encrypt produces the PKCS#7/CMS envelope Control4 expects for drivers
// that declare encryption="2" in driver.xml.
//
// Control4's Python tool used M2Crypto (OpenSSL) to build an SMIME/PKCS#7
// EnvelopedData: a random AES-256-CBC content key, the payload encrypted under
// it, and that key wrapped to the embedded "DriverWorks Encryption V2"
// certificate via RSA PKCS#1 v1.5. We reproduce the same envelope with the
// maintained pure-Go github.com/smallstep/pkcs7 library — no cgo, no openssl
// subprocess, and no hand-rolled crypto.
//
// The output is intentionally NOT byte-deterministic (the content key, IV and
// RSA padding are random per call); a Control4 director decrypts it with the
// matching private key. Byte-for-byte reproducibility is neither required nor
// attempted.
package encrypt

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"sync"

	"github.com/smallstep/pkcs7"
)

// Encryptor enveloped a plaintext Lua payload for inclusion in a .c4z.
type Encryptor interface {
	// Encrypt returns a DER-encoded PKCS#7 EnvelopedData over plaintext.
	Encrypt(plaintext []byte) ([]byte, error)
}

type driverWorks struct {
	cert *x509.Certificate
}

var (
	defaultOnce sync.Once
	defaultEnc  Encryptor
	defaultErr  error
)

// Default returns the production DriverWorks encryptor (AES-256-CBC content
// encryption, RSA PKCS#1 v1.5 key transport, to the embedded Control4 cert).
func Default() Encryptor {
	defaultOnce.Do(func() {
		block, _ := pem.Decode([]byte(driverWorksCertPEM))
		if block == nil {
			defaultErr = fmt.Errorf("encrypt: failed to decode embedded DriverWorks certificate")
			return
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			defaultErr = fmt.Errorf("encrypt: parse embedded DriverWorks certificate: %w", err)
			return
		}
		defaultEnc = &driverWorks{cert: cert}
	})
	if defaultErr != nil {
		return errEncryptor{defaultErr}
	}
	return defaultEnc
}

// encMu guards the package-global algorithm selectors in smallstep/pkcs7, which
// are set per call. A single CLI build is sequential; the lock makes concurrent
// in-process use safe regardless.
var encMu sync.Mutex

func (d *driverWorks) Encrypt(plaintext []byte) ([]byte, error) {
	encMu.Lock()
	defer encMu.Unlock()
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
	pkcs7.KeyEncryptionAlgorithm = pkcs7.OIDEncryptionAlgorithmRSA // RSA PKCS#1 v1.5 (matches OpenSSL SMIME)
	der, err := pkcs7.Encrypt(plaintext, []*x509.Certificate{d.cert})
	if err != nil {
		return nil, fmt.Errorf("encrypt: build PKCS#7 envelope: %w", err)
	}
	return der, nil
}

// errEncryptor defers a setup error to call time so Default never panics.
type errEncryptor struct{ err error }

func (e errEncryptor) Encrypt([]byte) ([]byte, error) { return nil, e.err }
