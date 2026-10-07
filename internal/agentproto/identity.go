package agentproto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"time"
)

const (
	secretBytes   = 32
	sniInfo       = "x-ui-agent sni v1"
	sniKeyLen     = 22
	identityYears = 10
)

var sniTLDs = []string{"com", "net", "org", "info", "io", "xyz", "online", "site"}

// NewSecret returns a fresh 256-bit secret, base64url without padding. It is
// both the bearer credential and the input of the SNI derivation.
func NewSecret() (string, error) {
	raw := make([]byte, secretBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// DecodeSecret accepts only the exact form NewSecret produces.
func DecodeSecret(secret string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(secret)
	if err != nil {
		return nil, fmt.Errorf("agent secret is not base64url: %w", err)
	}
	if len(raw) != secretBytes {
		return nil, fmt.Errorf("agent secret must be %d bytes, got %d", secretBytes, len(raw))
	}
	return raw, nil
}

// SNIFor derives the only server name the agent serves. It crosses the wire in
// clear text, so it filters scanners and authenticates nothing.
func SNIFor(secret string) (string, error) {
	raw, err := DecodeSecret(secret)
	if err != nil {
		return "", err
	}
	okm, err := hkdf.Key(sha256.New, raw, nil, sniInfo, sniKeyLen)
	if err != nil {
		return "", err
	}
	tld := sniTLDs[int(okm[sniKeyLen-1])%len(sniTLDs)]
	return hex.EncodeToString(okm[:16]) + "." + hex.EncodeToString(okm[16:21]) + "." + tld, nil
}

// CheckBearer reports whether an Authorization header carries the secret.
func CheckBearer(authorization, secret string) bool {
	scheme, token, ok := strings.Cut(strings.TrimSpace(authorization), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || secret == "" {
		return false
	}
	return equalConstantTime(strings.TrimSpace(token), secret)
}

// equalConstantTime hashes first so even the length of the secret is not leaked.
func equalConstantTime(a, b string) bool {
	ha := sha256.Sum256([]byte(a))
	hb := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ha[:], hb[:]) == 1
}

// Fingerprint is the lowercase hex SHA-256 of a DER certificate, the pin form
// the panel already accepts for nodes.
func Fingerprint(certDER []byte) string {
	sum := sha256.Sum256(certDER)
	return hex.EncodeToString(sum[:])
}

// Identity is a self-signed agent certificate with its private key.
type Identity struct {
	CertPEM     string
	KeyPEM      string
	Fingerprint string
}

// GenerateIdentity mints an ECDSA P-256 certificate valid for address and sni.
func GenerateIdentity(address, sni string, now time.Time) (*Identity, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "x-ui-agent"},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(identityYears, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{sni},
	}
	if ip := net.ParseIP(address); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else if address != "" {
		tpl.DNSNames = append(tpl.DNSNames, address)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	return &Identity{
		CertPEM:     string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		KeyPEM:      string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})),
		Fingerprint: Fingerprint(der),
	}, nil
}

var errNoCertificate = errors.New("agent certificate is not PEM encoded")

// certDER returns the first certificate of a PEM bundle.
func certDER(certPEM string) ([]byte, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errNoCertificate
	}
	return block.Bytes, nil
}
