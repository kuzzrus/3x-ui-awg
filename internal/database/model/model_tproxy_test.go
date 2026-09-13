package model

import "testing"

func TestClientToRecordRoundTripTproxy(t *testing.T) {
	c := &Client{
		Email:        "alice@example.test",
		Enable:       true,
		TproxySecret: "00112233445566778899aabbccddeeff",
	}

	rec := c.ToRecord()
	if rec.TproxySecret != c.TproxySecret {
		t.Fatalf("ToRecord: TproxySecret = %q, want %q", rec.TproxySecret, c.TproxySecret)
	}

	got := rec.ToClient()
	if got.TproxySecret != c.TproxySecret {
		t.Errorf("round-trip: TproxySecret = %q, want %q", got.TproxySecret, c.TproxySecret)
	}
}

func TestValidTproxySecret(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain lowercase", "00112233445566778899aabbccddeeff", true},
		{"plain uppercase", "00112233445566778899AABBCCDDEEFF", true},
		{"dd-prefixed", "dd00112233445566778899aabbccddeeff", true},
		{"DD-prefixed", "DD00112233445566778899AABBCCDDEEFF", true},
		{"34 chars without dd prefix", "ab0123456789abcdef0123456789abcdef", false},
		{"too short", "00112233", false},
		{"not hex", "gg112233445566778899aabbccddeeff", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ValidTproxySecret(c.in); got != c.want {
				t.Errorf("ValidTproxySecret(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
