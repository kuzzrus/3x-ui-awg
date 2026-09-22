package tproxy

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestInstanceFromInbound(t *testing.T) {
	ib := &model.Inbound{
		Id:       3,
		Protocol: model.Tproxy,
		Settings: `{"clients":[` +
			`{"email":"alice","tproxySecret":"00112233445566778899aabbccddeeff","enable":true},` +
			`{"email":"bob","tproxySecret":"","enable":true},` +
			`{"email":"","tproxySecret":"ffeeddccbbaa99887766554433221100","enable":true},` +
			`{"email":"carol","tproxySecret":"0123456789abcdef0123456789abcdef","enable":false}]}`,
	}
	inst, ok := InstanceFromInbound(ib)
	if !ok {
		t.Fatal("expected a usable instance")
	}
	if inst.Id != 3 {
		t.Fatalf("Id = %d, want 3", inst.Id)
	}
	if len(inst.Clients) != 1 {
		t.Fatalf("only the enabled client with both an email and a secret should be served, got %d: %+v", len(inst.Clients), inst.Clients)
	}
	if inst.Clients[0].Name != "alice" || inst.Clients[0].Secret != "00112233445566778899aabbccddeeff" {
		t.Fatalf("unexpected client: %+v", inst.Clients[0])
	}
}

func TestInstanceFromInboundRejectsNonTproxyOrEmpty(t *testing.T) {
	if _, ok := InstanceFromInbound(nil); ok {
		t.Fatal("nil inbound should not produce an instance")
	}
	if _, ok := InstanceFromInbound(&model.Inbound{Protocol: model.VLESS}); ok {
		t.Fatal("non-tproxy inbound should not produce an instance")
	}
	noSecrets := &model.Inbound{Protocol: model.Tproxy, Settings: `{"clients":[{"email":"x","tproxySecret":"","enable":true}]}`}
	if _, ok := InstanceFromInbound(noSecrets); ok {
		t.Fatal("an inbound with no active client should not produce an instance")
	}
}
