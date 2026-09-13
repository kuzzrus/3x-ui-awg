package tproxy

import (
	"strings"
	"testing"
)

// Vectors verified against telegramdesktop/tproxy-server's own PROTOCOL.md
// and DeriveCapability (commit f7a6acc4d536a787d442fd7df3ba4ebfd728f406).
func TestDeriveCapabilityPublishedVectors(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		want   string
	}{
		{"plain", "000102030405060708090a0b0c0d0e0f", "MHLEY5PmW1GWqJkSrlmJpvJUiLhBH_QKy6yKg8a0JPk"},
		{"dd-prefixed", "dd000102030405060708090a0b0c0d0e0f", "IpJrt3e7sKtzPyoXy6w-Zj6GGEvsvclN66JzQEfPYLA"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DeriveCapability("proxy.example.com", c.secret)
			if err != nil {
				t.Fatalf("DeriveCapability: %v", err)
			}
			if got != c.want {
				t.Errorf("DeriveCapability(%q) = %q, want %q", c.secret, got, c.want)
			}
		})
	}
}

func TestDeriveCapabilityNormalizesHostnameCase(t *testing.T) {
	lower, err := DeriveCapability("proxy.example.com", "000102030405060708090a0b0c0d0e0f")
	if err != nil {
		t.Fatalf("DeriveCapability: %v", err)
	}
	upper, err := DeriveCapability("PROXY.EXAMPLE.COM", "000102030405060708090a0b0c0d0e0f")
	if err != nil {
		t.Fatalf("DeriveCapability: %v", err)
	}
	if lower != upper {
		t.Errorf("hostname case should be normalized before hashing: %q != %q", lower, upper)
	}
}

func TestDeriveCapabilityRejectsMalformedSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		want   string
	}{
		{"wrong length", "not-a-secret", "client secret must be 32 hex digits, optionally dd-prefixed (got 12 characters)"},
		{"right length, not hex", strings.Repeat("z", 32), "client secret must be 32 hex digits, optionally dd-prefixed (got 32 characters)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DeriveCapability("proxy.example.com", c.secret)
			if err == nil || err.Error() != c.want {
				t.Errorf("DeriveCapability(%q) error = %v, want %q", c.secret, err, c.want)
			}
		})
	}
}
