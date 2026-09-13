package tproxy

import "testing"

// TestDeriveCapabilityPublishedVectors pins the implementation against
// PROTOCOL.md's own published test vectors, not just internal self-consistency.
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

func TestDeriveCapabilityDiffersByHostnameCase(t *testing.T) {
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
	if _, err := DeriveCapability("proxy.example.com", "not-a-secret"); err == nil {
		t.Fatal("DeriveCapability accepted a malformed secret")
	}
}
