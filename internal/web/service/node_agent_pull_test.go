package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const pullMaster = "https://panel.example.com:2096/secret/"

func TestCreateAgentInPullModeNamesTheNodeAndTheMasterInTheBundle(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Pull: true, MasterUrl: pullMaster, MasterPin: strings.Repeat("ab", 32)})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	bundle := parsePairing(t, pairing)
	if !bundle.Pull() || bundle.Node != pairing.Node.Id || bundle.Master != pullMaster || bundle.Pin != strings.Repeat("ab", 32) {
		t.Fatalf("bundle = %+v, want a pull bundle of node %d for %s", bundle, pairing.Node.Id, pullMaster)
	}
	if bundle.Address != "" || bundle.CertPEM != "" || bundle.KeyPEM != "" {
		t.Fatalf("a pull bundle carries the identity of an agent that listens: %+v", bundle)
	}

	stored, err := svc.GetById(pairing.Node.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Pull || stored.Kind != model.NodeKindAgent || stored.ApiToken != bundle.Secret {
		t.Fatalf("stored node = %+v, want an agent in pull mode holding the secret of the bundle", stored)
	}
	// Nothing reaches such a node, so it holds no pin and a port that only has to be a valid one, and a later save is not refused for them.
	if stored.TlsVerifyMode != "verify" || stored.PinnedCertSha256 != "" || stored.Port != pullNodePort {
		t.Fatalf("stored transport = %q %q %d", stored.TlsVerifyMode, stored.PinnedCertSha256, stored.Port)
	}
	if !pairing.Node.Pull {
		t.Fatal("the view of the new node does not say it is pulled")
	}
}

// The bundle can only be made once the node has an id, so a master that cannot be dialled has to be turned down
// before a row is made, or every refusal would leave a node behind that nobody has the bundle of.
func TestCreateAgentInPullModeLeavesNoNodeWhenTheMasterIsRefused(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	for name, req := range map[string]*AgentNodeRequest{
		"no master":    {Name: "a", Address: "203.0.113.7", Pull: true},
		"plain http":   {Name: "b", Address: "203.0.113.7", Pull: true, MasterUrl: "http://panel.example.com/"},
		"a bad pin":    {Name: "c", Address: "203.0.113.7", Pull: true, MasterUrl: pullMaster, MasterPin: "abcd"},
		"a user in it": {Name: "d", Address: "203.0.113.7", Pull: true, MasterUrl: "https://admin:pw@panel.example.com/"},
	} {
		if pairing, err := svc.CreateAgent(req); err == nil {
			t.Errorf("%s: a node was added: %+v", name, pairing)
		}
	}
	var count int64
	if err := database.GetDB().Model(model.Node{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("%d nodes are left after the refusals, %v", count, err)
	}
}

func TestPullAgentSecretIsOnlyForAnEnabledAgentThatDialsThePanel(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pulled, err := svc.CreateAgent(&AgentNodeRequest{Name: "pulled", Address: "203.0.113.7", Pull: true, MasterUrl: pullMaster})
	if err != nil {
		t.Fatal(err)
	}
	pushed, err := svc.CreateAgent(&AgentNodeRequest{Name: "pushed", Address: "203.0.113.8", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	panel := &model.Node{Name: "panel", Scheme: "https", Address: "203.0.113.9", Port: 2053, BasePath: "/", ApiToken: "token", Enable: true}
	if err := database.GetDB().Create(panel).Error; err != nil {
		t.Fatal(err)
	}

	secret, ok := svc.PullAgentSecret(pulled.Node.Id)
	if !ok || secret != parsePairing(t, pulled).Secret {
		t.Fatalf("PullAgentSecret = %q, %v, want the secret of the bundle", secret, ok)
	}
	for name, id := range map[string]int{"a node the panel dials": pushed.Node.Id, "a panel node": panel.Id, "a node that is not there": 9999, "no node": 0} {
		if secret, ok := svc.PullAgentSecret(id); ok || secret != "" {
			t.Errorf("%s: PullAgentSecret = %q, %v, want nothing", name, secret, ok)
		}
	}
	if err := svc.SetEnable(pulled.Node.Id, false); err != nil {
		t.Fatal(err)
	}
	if secret, ok := svc.PullAgentSecret(pulled.Node.Id); ok || secret != "" {
		t.Errorf("a disabled node: PullAgentSecret = %q, %v, want nothing", secret, ok)
	}
}

func TestRepairAgentKeepsThePairingWayUnlessItIsToldOtherwise(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pulled, err := svc.CreateAgent(&AgentNodeRequest{Name: "pulled", Address: "203.0.113.7", Pull: true, MasterUrl: pullMaster})
	if err != nil {
		t.Fatal(err)
	}
	id := pulled.Node.Id
	old := parsePairing(t, pulled)

	// With only where the master is, a node that dials stays one, with a new secret.
	again, err := svc.RepairAgent(id, &AgentRepairRequest{MasterUrl: pullMaster})
	if err != nil {
		t.Fatalf("RepairAgent: %v", err)
	}
	fresh := parsePairing(t, again)
	if !fresh.Pull() || fresh.Node != id || fresh.Secret == old.Secret {
		t.Fatalf("the new bundle = %+v, want a pull bundle of the same node with another secret", fresh)
	}
	if secret, ok := svc.PullAgentSecret(id); !ok || secret != fresh.Secret {
		t.Fatalf("the node accepts %q, %v, want only the new secret", secret, ok)
	}
	// Without it there is nothing to dial.
	if _, err := svc.RepairAgent(id, nil); err == nil {
		t.Fatal("a node that dials was paired again without the address of the master")
	}
	if secret, _ := svc.PullAgentSecret(id); secret != fresh.Secret {
		t.Fatal("a refused pairing changed the secret of the node")
	}

	// A node the panel dials stays one when nothing says otherwise.
	pushed, err := svc.CreateAgent(&AgentNodeRequest{Name: "pushed", Address: "203.0.113.8", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	same, err := svc.RepairAgent(pushed.Node.Id, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bundle := parsePairing(t, same); bundle.Pull() || bundle.Port != 8443 || same.Node.Pull {
		t.Fatalf("a push node paired again with nothing said became %+v", bundle)
	}
}

func TestRepairingANodeDropsTheConnectionOfItsOldAgent(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pulled, err := svc.CreateAgent(&AgentNodeRequest{Name: "pulled", Address: "203.0.113.7", Pull: true, MasterUrl: pullMaster})
	if err != nil {
		t.Fatal(err)
	}
	id := pulled.Node.Id
	tunnels := runtime.GetAgentTunnels()
	t.Cleanup(func() { tunnels.Drop(id) })
	attachFor(t, tunnels, id)
	if !tunnels.Connected(id) {
		t.Fatal("setup: the agent is not connected")
	}

	if _, err := svc.RepairAgent(id, &AgentRepairRequest{MasterUrl: pullMaster}); err != nil {
		t.Fatal(err)
	}
	if tunnels.Connected(id) {
		t.Fatal("the agent of the old secret is still connected after the node was paired again")
	}
}

// attachFor connects a stand-in agent to tunnels as the agent of node, and waits until it is registered.
func attachFor(t *testing.T, tunnels *runtime.AgentTunnels, node int) {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{agentproto.ConnectProtocol}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := upgrader.Upgrade(w, r, nil); err == nil {
			_ = tunnels.Attach(node, ws)
		}
	}))
	t.Cleanup(srv.Close)
	dialer := websocket.Dialer{Subprotocols: []string{agentproto.ConnectProtocol}}
	ws, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := agentproto.NewConn(ws, nil)
	t.Cleanup(func() { conn.Close() })
	go agentproto.ServeConn(context.Background(), conn, &http.Server{Handler: http.NotFoundHandler()})
	for deadline := time.Now().Add(10 * time.Second); !tunnels.Connected(node); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the stand-in agent was never registered")
		}
	}
}
