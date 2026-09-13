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
