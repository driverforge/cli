package encrypt

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

// The algorithm OIDs a Control4 director requires on the wire. These are the
// contract: the director's private key and OpenSSL build decide what it can
// decrypt, and we get no feedback from it in CI. Encrypt() sets the smallstep
// package-global selectors per call, so an upstream default change (or a
// dropped assignment) would still produce a well-formed, round-trippable
// envelope — the existing round-trip test would pass while every Director
// rejected the driver. Decoding the DER by hand is the only check that fails.
var (
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}      // pkcs7-envelopedData
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}      // pkcs7-data
	oidAES256CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42} // aes256-CBC
	oidRSAEncryption = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}      // rsaEncryption (PKCS#1 v1.5)
	oidRSAESOAEP     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 7}      // must NOT appear
	oidAES128CBC     = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}  // must NOT appear
	oidDESEDE3CBC    = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}         // must NOT appear
)

// RFC 5652 ContentInfo/EnvelopedData, decoded independently of the library that
// produced it. Trailing fields are RawValue so BER-ish encodings still parse.
type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type issuerAndSerial struct {
	Issuer       asn1.RawValue
	SerialNumber *big.Int
}

type recipientInfo struct {
	Version                int
	IssuerAndSerialNumber  issuerAndSerial
	KeyEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedKey           []byte
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"tag:0,optional"`
}

type envelopedData struct {
	Version              int
	RecipientInfos       []recipientInfo `asn1:"set"`
	EncryptedContentInfo encryptedContentInfo
}

func decodeEnvelope(t *testing.T, der []byte) envelopedData {
	t.Helper()
	var ci contentInfo
	rest, err := asn1.Unmarshal(der, &ci)
	if err != nil {
		t.Fatalf("decode ContentInfo: %v", err)
	}
	if len(rest) != 0 {
		t.Errorf("%d trailing bytes after ContentInfo", len(rest))
	}
	if !ci.ContentType.Equal(oidEnvelopedData) {
		t.Fatalf("contentType = %v, want envelopedData %v", ci.ContentType, oidEnvelopedData)
	}
	var ed envelopedData
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &ed); err != nil {
		t.Fatalf("decode EnvelopedData: %v", err)
	}
	return ed
}

// testEncryptor builds a driverWorks over a throwaway self-signed cert, so the
// envelope's shape can be inspected without depending on the embedded cert.
func testEncryptor(t *testing.T) (*driverWorks, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x2a),
		Subject:      pkix.Name{CommonName: "envelope-test"},
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
	return &driverWorks{cert: cert}, cert
}

// The content must be encrypted under AES-256-CBC with a 16-byte IV, carried as
// pkcs7-data. Anything else — a shorter AES variant, 3DES, a missing IV — is a
// driver the Director cannot open.
func TestEnvelopeContentEncryptionIsAES256CBC(t *testing.T) {
	enc, _ := testEncryptor(t)
	der, err := enc.Encrypt([]byte("local x = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	ed := decodeEnvelope(t, der)

	eci := ed.EncryptedContentInfo
	if !eci.ContentType.Equal(oidData) {
		t.Errorf("encryptedContentInfo.contentType = %v, want data %v", eci.ContentType, oidData)
	}
	alg := eci.ContentEncryptionAlgorithm.Algorithm
	if !alg.Equal(oidAES256CBC) {
		t.Fatalf("content encryption = %v, want aes256-CBC %v", alg, oidAES256CBC)
	}
	for _, bad := range []asn1.ObjectIdentifier{oidAES128CBC, oidDESEDE3CBC} {
		if alg.Equal(bad) {
			t.Fatalf("content encryption fell back to %v", bad)
		}
	}
	// The CBC parameters are the IV, an OCTET STRING of exactly one AES block.
	var iv []byte
	if _, err := asn1.Unmarshal(eci.ContentEncryptionAlgorithm.Parameters.FullBytes, &iv); err != nil {
		t.Fatalf("decode CBC IV: %v", err)
	}
	if len(iv) != 16 {
		t.Errorf("IV length = %d, want 16", len(iv))
	}
}

// The content key must be wrapped with RSA PKCS#1 v1.5 (rsaEncryption), the
// scheme Control4's OpenSSL SMIME invocation uses and the only one the Director's key
// handling accepts. RSAES-OAEP would round-trip locally and fail in the field.
func TestEnvelopeKeyTransportIsRSAPKCS1v15(t *testing.T) {
	enc, cert := testEncryptor(t)
	der, err := enc.Encrypt([]byte("local x = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	ed := decodeEnvelope(t, der)

	if len(ed.RecipientInfos) != 1 {
		t.Fatalf("recipientInfos = %d, want exactly 1", len(ed.RecipientInfos))
	}
	ri := ed.RecipientInfos[0]
	if !ri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAEncryption) {
		t.Fatalf("key encryption = %v, want rsaEncryption %v",
			ri.KeyEncryptionAlgorithm.Algorithm, oidRSAEncryption)
	}
	if ri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAESOAEP) {
		t.Fatal("key transport used RSAES-OAEP; the Director requires PKCS#1 v1.5")
	}
	// The wrapped key is a full RSA block for the recipient's 2048-bit modulus.
	if got := len(ri.EncryptedKey); got != 256 {
		t.Errorf("encryptedKey length = %d, want 256", got)
	}
	// It must be addressed to the cert we encrypted to.
	if ri.IssuerAndSerialNumber.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Errorf("recipient serial = %v, want %v", ri.IssuerAndSerialNumber.SerialNumber, cert.SerialNumber)
	}
	if string(ri.IssuerAndSerialNumber.Issuer.FullBytes) != string(cert.RawIssuer) {
		t.Error("recipient issuer does not match the certificate")
	}
}

// The production encryptor — the embedded DriverWorks cert — must produce the
// same envelope and address it to that certificate. This is what actually ships.
func TestDefaultEnvelopeTargetsDriverWorksCert(t *testing.T) {
	block, _ := pem.Decode([]byte(driverWorksCertPEM))
	if block == nil {
		t.Fatal("embedded DriverWorks certificate does not decode")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}

	der, err := Default().Encrypt([]byte("local x = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	ed := decodeEnvelope(t, der)

	if ed.Version != 0 {
		t.Errorf("EnvelopedData version = %d, want 0", ed.Version)
	}
	if !ed.EncryptedContentInfo.ContentEncryptionAlgorithm.Algorithm.Equal(oidAES256CBC) {
		t.Errorf("production content encryption = %v, want aes256-CBC",
			ed.EncryptedContentInfo.ContentEncryptionAlgorithm.Algorithm)
	}
	if len(ed.RecipientInfos) != 1 {
		t.Fatalf("recipientInfos = %d, want exactly 1", len(ed.RecipientInfos))
	}
	ri := ed.RecipientInfos[0]
	if !ri.KeyEncryptionAlgorithm.Algorithm.Equal(oidRSAEncryption) {
		t.Errorf("production key transport = %v, want rsaEncryption", ri.KeyEncryptionAlgorithm.Algorithm)
	}
	if ri.IssuerAndSerialNumber.SerialNumber.Cmp(cert.SerialNumber) != 0 {
		t.Errorf("envelope addressed to serial %v, want the DriverWorks cert's %v",
			ri.IssuerAndSerialNumber.SerialNumber, cert.SerialNumber)
	}
	if string(ri.IssuerAndSerialNumber.Issuer.FullBytes) != string(cert.RawIssuer) {
		t.Error("envelope is not addressed to the embedded DriverWorks certificate")
	}
}

// The plaintext must not survive anywhere in the envelope, and two calls must
// differ (fresh content key and IV per call) — the documented non-determinism.
func TestEnvelopeHidesPlaintextAndIsFreshPerCall(t *testing.T) {
	enc, _ := testEncryptor(t)
	plaintext := []byte("local secret = 'do not ship me in the clear'\n")

	first, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}

	for i, der := range [][]byte{first, second} {
		if bytesContains(der, plaintext) {
			t.Errorf("envelope %d contains the plaintext", i)
		}
	}
	if string(first) == string(second) {
		t.Error("two envelopes over the same plaintext are identical; the content key/IV is not fresh")
	}
}

func bytesContains(haystack, needle []byte) bool {
	if len(needle) == 0 || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
