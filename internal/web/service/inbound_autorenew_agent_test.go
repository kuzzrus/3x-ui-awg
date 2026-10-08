package service

import (
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// seedDueClient is a monthly client, switched off and past its expiry, on a new inbound of the node
// (or a local one when nodeID is nil).
func seedDueClient(t *testing.T, nodeID *int, port int, email string) *model.Inbound {
	t.Helper()
	db := database.GetDB()
	past := time.Now().Add(-48 * time.Hour).UnixMilli()
	clients := []model.Client{{Email: email, ID: "11111111-1111-1111-1111-111111111111", Enable: false, Reset: 30, ExpiryTime: past}}
	ib := mkInbound(t, port, model.VLESS, clientsSettings(t, clients))
	if nodeID != nil {
		if err := db.Model(ib).Update("node_id", *nodeID).Error; err != nil {
			t.Fatal(err)
		}
		ib.NodeID = nodeID
	}
	if err := (&ClientService{}).SyncInbound(nil, ib.Id, clients); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}
	if err := db.Create(&xray.ClientTraffic{
		InboundId: ib.Id, Email: email, Enable: false, Up: 100, Down: 200, Reset: 30, ExpiryTime: past,
	}).Error; err != nil {
		t.Fatalf("seed client_traffics: %v", err)
	}
	return ib
}

// An agent has no panel to renew its clients, so this one does, and the renewed client is in the
// config the agent is pushed next.
func TestAutoRenewClientsRenewsAClientThatLivesOnlyOnAnAgentNode(t *testing.T) {
	setupSettingTestDB(t)
	svc := &InboundService{}
	db := database.GetDB()
	nodeID := seedAgentRow(t, "agent")
	ib := seedDueClient(t, &nodeID, 30401, "monthly@x")

	batch := newTrafficMutationBatch()
	if _, count, err := svc.autoRenewClients(db, batch); err != nil {
		t.Fatalf("autoRenewClients: %v", err)
	} else if count != 1 {
		t.Fatalf("renewed count = %d, want 1: nobody else renews a client of an agent", count)
	}

	var traffic xray.ClientTraffic
	if err := db.Where("email = ?", "monthly@x").First(&traffic).Error; err != nil {
		t.Fatal(err)
	}
	if !traffic.Enable || traffic.ExpiryTime <= time.Now().UnixMilli() || traffic.ResetCount != 1 || traffic.Up != 0 || traffic.Down != 0 {
		t.Fatalf("client_traffics row = %+v, want it renewed, switched on and its usage cleared", traffic)
	}
	if _, marked := batch.nodeIDs[nodeID]; !marked || len(batch.localPlans) != 0 {
		t.Fatalf("node marked = %v, local plans = %d, want the agent marked for its next push and no call to the local core", marked, len(batch.localPlans))
	}
	cfg, err := (&XrayService{}).GetAgentConfig(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.InboundConfigs {
		if in.Tag == ib.Tag && strings.Contains(string(in.Settings), `"monthly@x"`) {
			return
		}
	}
	t.Fatal("the config rendered for the agent does not carry the renewed client")
}

func TestAutoRenewClientsLeavesTheClientsOfAPanelNodeToThatPanel(t *testing.T) {
	setupSettingTestDB(t)
	db := database.GetDB()
	node := &model.Node{Name: "panel", Scheme: "https", Address: "192.0.2.20", Port: 2053, BasePath: "/", Enable: true}
	seedNodeRow(t, db, node)
	seedDueClient(t, &node.Id, 30402, "remote@x")

	if _, count, err := (&InboundService{}).autoRenewClients(db, newTrafficMutationBatch()); err != nil {
		t.Fatalf("autoRenewClients: %v", err)
	} else if count != 0 {
		t.Fatalf("renewed count = %d, want 0: the panel of the node renews its own clients", count)
	}
}
