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
	// Must satisfy internal/tproxy's own normalizeSecret, not just be 32 characters.
	if !model.ValidTproxySecret(c.TproxySecret) || len(c.TproxySecret) != 32 {
		t.Fatalf("tproxy secret = %q, want a well-formed 32-hex-digit secret", c.TproxySecret)
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
