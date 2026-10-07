package agentproto

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"net"
	"regexp"
	"slices"
	"testing"
	"time"
)

func TestNewSecret(t *testing.T) {
	a, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	b, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	if a == b {
		t.Fatal("two secrets are identical")
	}
	if len(a) != 43 {
		t.Fatalf("secret length = %d, want 43", len(a))
	}
	raw, err := DecodeSecret(a)
	if err != nil || len(raw) != 32 {
		t.Fatalf("DecodeSecret = %d bytes, %v; want 32 bytes", len(raw), err)
	}
}

func TestDecodeSecretRejects(t *testing.T) {
	tests := []struct {
		name   string
		secret string
	}{
		{"empty", ""},
		{"too short", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh"},
		{"too long", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8AAAAA"},
		{"padded", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8="},
		{"standard alphabet", "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh+"},
		{"not base64", "not a secret!!"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeSecret(tt.secret); err == nil {
				t.Fatalf("DecodeSecret(%q) accepted a malformed secret", tt.secret)
			}
		})
	}
}

func TestSNIForKnownAnswers(t *testing.T) {
	// Vectors computed independently with Node's crypto.hkdfSync (SHA-256,
	// empty salt, info "x-ui-agent sni v1", 22 bytes).
	tests := []struct {
		secret string
		want   string
	}{
		{"AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", "810ea368e26b30c0081b85da8b053131.46a47e74f4.net"},
		{"__________________________________________8", "368b889bf1f789960e1795816f7f37ac.7357f7a4a7.xyz"},
	}
	for _, tt := range tests {
		got, err := SNIFor(tt.secret)
		if err != nil {
			t.Fatalf("SNIFor(%q): %v", tt.secret, err)
		}
		if got != tt.want {
			t.Fatalf("SNIFor(%q) = %q, want %q", tt.secret, got, tt.want)
		}
	}
}

func TestSNIForShapeAndDistinctness(t *testing.T) {
	shape := regexp.MustCompile(`^[0-9a-f]{32}\.[0-9a-f]{10}\.[a-z]+$`)
	seen := map[string]bool{}
	for range 20 {
		secret, err := NewSecret()
		if err != nil {
			t.Fatalf("NewSecret: %v", err)
		}
		sni, err := SNIFor(secret)
		if err != nil {
			t.Fatalf("SNIFor: %v", err)
		}
		if !shape.MatchString(sni) {
			t.Fatalf("SNI %q does not look like a host name", sni)
		}
		again, _ := SNIFor(secret)
		if again != sni {
			t.Fatalf("SNI is not deterministic: %q vs %q", sni, again)
		}
		if seen[sni] {
			t.Fatalf("SNI %q repeated across secrets", sni)
		}
		seen[sni] = true
	}
	if _, err := SNIFor("short"); err == nil {
		t.Fatal("SNIFor accepted an invalid secret")
	}
}

func TestCheckBearer(t *testing.T) {
	const secret = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8"
	tests := []struct {
		name   string
		header string
		secret string
		want   bool
	}{
		{"exact", "Bearer " + secret, secret, true},
		{"lowercase scheme", "bearer " + secret, secret, true},
		{"surrounding spaces", "  Bearer   " + secret + "  ", secret, true},
		{"wrong token", "Bearer nope", secret, false},
		{"prefix of the secret", "Bearer " + secret[:20], secret, false},
		{"no scheme", secret, secret, false},
		{"other scheme", "Basic " + secret, secret, false},
		{"empty header", "", secret, false},
		{"empty secret never matches", "Bearer ", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckBearer(tt.header, tt.secret); got != tt.want {
				t.Fatalf("CheckBearer(%q) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func parseIdentityCert(t *testing.T, id *Identity) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode([]byte(id.CertPEM))
	if block == nil {
		t.Fatal("identity certificate is not PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	return cert
}

func TestGenerateIdentity(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	const sni = "810ea368e26b30c0081b85da8b053131.46a47e74f4.net"

	tests := []struct {
		name    string
		address string
		wantIP  string
		wantDNS []string
	}{
		{"ip address", "203.0.113.7", "203.0.113.7", []string{sni}},
		{"ipv6 address", "2001:db8::7", "2001:db8::7", []string{sni}},
		{"host name", "node1.example.com", "", []string{sni, "node1.example.com"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := GenerateIdentity(tt.address, sni, now)
			if err != nil {
				t.Fatalf("GenerateIdentity: %v", err)
			}
			cert := parseIdentityCert(t, id)

			pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
			if !ok || pub.Curve != elliptic.P256() {
				t.Fatalf("public key = %T, want ECDSA P-256", cert.PublicKey)
			}
			if err := cert.CheckSignature(cert.SignatureAlgorithm, cert.RawTBSCertificate, cert.Signature); err != nil {
				t.Fatalf("certificate is not signed by its own key: %v", err)
			}
			if !slices.Equal(cert.DNSNames, tt.wantDNS) {
				t.Fatalf("DNS SANs = %v, want %v", cert.DNSNames, tt.wantDNS)
			}
			if tt.wantIP == "" {
				if len(cert.IPAddresses) != 0 {
					t.Fatalf("unexpected IP SANs %v", cert.IPAddresses)
				}
			} else if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP(tt.wantIP)) {
				t.Fatalf("IP SANs = %v, want [%s]", cert.IPAddresses, tt.wantIP)
			}
			if !cert.NotBefore.Before(now) || !cert.NotAfter.After(now.AddDate(9, 0, 0)) {
				t.Fatalf("validity %v..%v does not bracket now with a long tail", cert.NotBefore, cert.NotAfter)
			}

			sum := sha256.Sum256(cert.Raw)
			if id.Fingerprint != hex.EncodeToString(sum[:]) {
				t.Fatalf("fingerprint = %s, want sha256 of the DER %x", id.Fingerprint, sum)
			}
			if _, err := tls.X509KeyPair([]byte(id.CertPEM), []byte(id.KeyPEM)); err != nil {
				t.Fatalf("key does not match certificate: %v", err)
			}
		})
	}
}

func TestGenerateIdentityIsUnique(t *testing.T) {
	now := time.Now()
	a, err := GenerateIdentity("203.0.113.7", "a.example", now)
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	b, err := GenerateIdentity("203.0.113.7", "a.example", now)
	if err != nil {
		t.Fatalf("GenerateIdentity: %v", err)
	}
	if a.Fingerprint == b.Fingerprint || a.KeyPEM == b.KeyPEM {
		t.Fatal("two identities share a key or fingerprint")
	}
}
