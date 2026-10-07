package agentproto

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func newTestBundle(t *testing.T) (*Bundle, string) {
	t.Helper()
	b, fingerprint, err := NewBundle("203.0.113.7", 8443, time.Now())
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	return b, fingerprint
}

// rawToken encodes v like Bundle.Encode does but without validating it, so a
// test can hand ParseBundle a token the master would never have produced.
func rawToken(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	return BundlePrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func TestBundleRoundTrip(t *testing.T) {
	b, fingerprint := newTestBundle(t)
	token, err := b.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasPrefix(token, BundlePrefix) {
		t.Fatalf("token %q lacks the %q prefix", token, BundlePrefix)
	}
	if strings.ContainsAny(token, " \r\n+/=") {
		t.Fatalf("token is not one base64url word: %q", token)
	}

	got, err := ParseBundle("\n  " + token + " \r\n")
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	if *got != *b {
		t.Fatalf("round trip changed the bundle:\n got %+v\nwant %+v", got, b)
	}
	if gotFP, err := got.Fingerprint(); err != nil || gotFP != fingerprint {
		t.Fatalf("Fingerprint = %q, %v; want %q", gotFP, err, fingerprint)
	}
	wantSNI, err := SNIFor(b.Secret)
	if err != nil {
		t.Fatalf("SNIFor: %v", err)
	}
	if gotSNI, err := got.SNI(); err != nil || gotSNI != wantSNI {
		t.Fatalf("SNI = %q, %v; want %q", gotSNI, err, wantSNI)
	}
}

func TestNewBundleRejectsBadEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    int
	}{
		{"port zero", "203.0.113.7", 0},
		{"port too large", "203.0.113.7", 65536},
		{"empty address", "", 8443},
		{"url instead of host", "https://example.com", 8443},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, fingerprint, err := NewBundle(tt.address, tt.port, time.Now())
			if err == nil || b != nil || fingerprint != "" {
				t.Fatalf("NewBundle(%q, %d) = %v, %q, %v; want an error", tt.address, tt.port, b, fingerprint, err)
			}
		})
	}
}

func TestEncodeRejectsInvalidBundle(t *testing.T) {
	good, _ := newTestBundle(t)
	badPort := *good
	badPort.Port = 0

	tests := []struct {
		name   string
		bundle *Bundle
	}{
		{"zero value", &Bundle{}},
		{"bad port", &badPort},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if token, err := tt.bundle.Encode(); err == nil {
				t.Fatalf("Encode produced %q for an invalid bundle", token)
			}
		})
	}
}

func TestParseBundleRejects(t *testing.T) {
	good, _ := newTestBundle(t)
	other, _ := newTestBundle(t)
	goodToken, err := good.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	body := strings.TrimPrefix(goodToken, BundlePrefix)
	with := func(mutate func(*Bundle)) string {
		c := *good
		mutate(&c)
		return rawToken(t, c)
	}

	tests := []struct {
		name  string
		token string
	}{
		{"empty", ""},
		{"prefix only", BundlePrefix},
		{"other format version", "xab2." + body},
		{"no prefix", body},
		{"padded", goodToken + "="},
		{"not json", BundlePrefix + base64.RawURLEncoding.EncodeToString([]byte("not json"))},
		{"oversized", BundlePrefix + strings.Repeat("A", base64.RawURLEncoding.EncodedLen(maxBundleBytes)+1)},
		{"unsupported version", with(func(b *Bundle) { b.Version = 2 })},
		{"missing version", with(func(b *Bundle) { b.Version = 0 })},
		{"port zero", with(func(b *Bundle) { b.Port = 0 })},
		{"port too large", with(func(b *Bundle) { b.Port = 70000 })},
		{"bad address", with(func(b *Bundle) { b.Address = "not a host" })},
		{"short secret", with(func(b *Bundle) { b.Secret = "abc" })},
		{"certificate not PEM", with(func(b *Bundle) { b.CertPEM = "nope" })},
		{"key from another bundle", with(func(b *Bundle) { b.KeyPEM = other.KeyPEM })},
		{"empty key", with(func(b *Bundle) { b.KeyPEM = "" })},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := ParseBundle(tt.token); err == nil {
				t.Fatalf("ParseBundle accepted the token and returned %+v", got)
			}
		})
	}
}

func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		address string
		port    int
		ok      bool
	}{
		{"ipv4", "203.0.113.7", 8443, true},
		{"ipv6", "2001:db8::1", 8443, true},
		{"host name", "node1.example.com", 8443, true},
		{"single label", "localhost", 8443, true},
		{"punycode", "xn--80ak6aa92e.com", 8443, true},
		{"mixed case", "Node1.Example.COM", 8443, true},
		{"lowest port", "203.0.113.7", 1, true},
		{"highest port", "203.0.113.7", 65535, true},
		{"port zero", "203.0.113.7", 0, false},
		{"negative port", "203.0.113.7", -1, false},
		{"port too large", "203.0.113.7", 65536, false},
		{"ipv6 zone", "fe80::1%eth0", 8443, false},
		{"empty", "", 8443, false},
		{"leading hyphen", "-bad.example.com", 8443, false},
		{"trailing hyphen", "bad-.example.com", 8443, false},
		{"underscore", "bad_host.example.com", 8443, false},
		{"empty label", "example..com", 8443, false},
		{"trailing dot", "example.com.", 8443, false},
		{"space", "bad host", 8443, false},
		{"scheme", "http://example.com", 8443, false},
		{"host with port", "example.com:8443", 8443, false},
		{"label too long", strings.Repeat("a", 64) + ".com", 8443, false},
		{"name too long", strings.Repeat("a.", 130) + "com", 8443, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEndpoint(tt.address, tt.port)
			if (err == nil) != tt.ok {
				t.Fatalf("validateEndpoint(%q, %d) = %v, want ok=%v", tt.address, tt.port, err, tt.ok)
			}
		})
	}
}
