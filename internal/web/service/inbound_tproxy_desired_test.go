package service

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/tproxy"
)

func TestDesiredTproxyInstancesFiltersDepleted(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	const (
		secretAlice = "00112233445566778899aabbccddeeff"
		secretBob   = "ffeeddccbbaa99887766554433221100"
		secretCarol = "0123456789abcdef0123456789abcdef"
	)

	seedInboundConflict(t, "tp-desired", "", 47001, model.Tproxy,
		"",
		`{"clients":[`+
			`{"email":"alice","tproxySecret":"`+secretAlice+`","enable":true},`+
			`{"email":"bob","tproxySecret":"`+secretBob+`","enable":true},`+
			`{"email":"carol","tproxySecret":"`+secretCarol+`","enable":false}]}`)
	served := loadInboundByTag(t, "tp-desired")
	seedClientTraffic(t, served.Id, "alice", true)
	seedClientTraffic(t, served.Id, "bob", false)
	seedClientTraffic(t, served.Id, "carol", true)

	seedInboundConflict(t, "tp-all-depleted", "", 47002, model.Tproxy,
		"",
		`{"clients":[{"email":"dave","tproxySecret":"`+secretAlice+`","enable":true}]}`)
	depleted := loadInboundByTag(t, "tp-all-depleted")
	seedClientTraffic(t, depleted.Id, "dave", false)

	nodeID := 5
	seedInboundConflictNode(t, "tp-node-owned", "", 47003, model.Tproxy,
		"",
		`{"clients":[{"email":"erin","tproxySecret":"`+secretBob+`","enable":true}]}`,
		&nodeID)

	instances, err := svc.DesiredTproxyInstances()
	if err != nil {
		t.Fatalf("DesiredTproxyInstances: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("expected exactly the served inbound, got %d instances: %+v", len(instances), instances)
	}
	if instances[0].Id != served.Id {
		t.Fatalf("expected inbound %d, got %d", served.Id, instances[0].Id)
	}
	want := []tproxy.ClientSecret{{Name: "alice", Secret: secretAlice}}
	if !reflect.DeepEqual(instances[0].Clients, want) {
		t.Fatalf("served clients: got %+v, want %+v", instances[0].Clients, want)
	}
}
