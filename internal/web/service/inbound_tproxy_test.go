package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestFillProtocolDefaultsTproxy(t *testing.T) {
	cs := &ClientService{}
	ib := &model.Inbound{Protocol: model.Tproxy}

	c := &model.Client{Email: "u"}
	if err := cs.fillProtocolDefaults(c, ib); err != nil {
		t.Fatal(err)
	}
	// Must decode as the plain 32-hex-digit form MTProxy's own -S flag (and
	// this package's normalizeSecret) accept -- not just any 32 characters.
	if len(c.TproxySecret) != 32 {
		t.Fatalf("tproxy secret = %q, want 32 characters", c.TproxySecret)
	}
	for _, r := range c.TproxySecret {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			t.Fatalf("tproxy secret = %q, want lowercase hex digits only", c.TproxySecret)
		}
	}

	// An existing secret is not overwritten.
	pre := &model.Client{Email: "v", TproxySecret: "00112233445566778899aabbccddeeff"}
	if err := cs.fillProtocolDefaults(pre, ib); err != nil {
		t.Fatal(err)
	}
	if pre.TproxySecret != "00112233445566778899aabbccddeeff" {
		t.Fatalf("an existing secret must be preserved, got %q", pre.TproxySecret)
	}
}
