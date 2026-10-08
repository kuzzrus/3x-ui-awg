package service

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func parsePairing(t *testing.T, pairing *AgentPairing) *agentproto.Bundle {
	t.Helper()
	bundle, err := agentproto.ParseBundle(pairing.Bundle)
	if err != nil {
		t.Fatalf("the bundle handed back does not parse: %v", err)
	}
	return bundle
}

func bundlePin(t *testing.T, bundle *agentproto.Bundle) string {
	t.Helper()
	block, _ := pem.Decode([]byte(bundle.CertPEM))
	if block == nil {
		t.Fatal("the bundle carries no certificate")
	}
	return agentproto.Fingerprint(block.Bytes)
}

// pointAt moves the node to where a test agent listens, since the bundle names an
// address the test cannot bind.
func pointAt(t *testing.T, nodeID, port int) *model.Node {
	t.Helper()
	if err := database.GetDB().Model(model.Node{}).Where("id = ?", nodeID).Update("port", port).Error; err != nil {
		t.Fatal(err)
	}
	node, err := (&NodeService{}).GetById(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestCreateAgentMintsTheSecretAndThePinOfTheBundle(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}

	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Remark: "fra", Address: "203.0.113.7", Port: 8443, AllowPrivateAddress: true})
	if err != nil {
		t.Fatalf("CreateAgent: %v", err)
	}
	bundle := parsePairing(t, pairing)
	view := pairing.Node
	if view.Kind != model.NodeKindAgent || view.Scheme != "https" || view.TlsVerifyMode != "pin" || view.BasePath != "/" ||
		!view.Enable || !view.HasApiToken || !view.ConfigDirty || !view.AllowPrivateAddress {
		t.Fatalf("view = %+v, want an enabled, dirty agent reached over https with a pinned certificate", view)
	}
	if bundle.Address != "203.0.113.7" || bundle.Port != 8443 {
		t.Fatalf("bundle names %s:%d, want the endpoint that was asked for", bundle.Address, bundle.Port)
	}
	if want := bundlePin(t, bundle); view.PinnedCertSha256 != want {
		t.Fatalf("node pins %q, the bundle's certificate is %q", view.PinnedCertSha256, want)
	}

	stored, err := svc.GetById(view.Id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ApiToken != bundle.Secret || stored.Kind != model.NodeKindAgent {
		t.Fatalf("stored node = kind %q token matches bundle %v, want the agent and the secret of its bundle", stored.Kind, stored.ApiToken == bundle.Secret)
	}

	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), bundle.Secret) || strings.Contains(string(raw), "xab1.") {
		t.Fatalf("the node view carries the secret or the bundle: %s", raw)
	}
	again, err := svc.GetViewById(view.Id)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(again); strings.Contains(string(raw), bundle.Secret) {
		t.Fatalf("the bundle can be read back from the node: %s", raw)
	}
}

func TestCreateAgentRefusesWhatIsNotAnEndpoint(t *testing.T) {
	setupConflictDB(t)
	tests := []struct {
		name string
		req  AgentNodeRequest
	}{
		{"no name", AgentNodeRequest{Address: "203.0.113.7", Port: 8443}},
		{"no address", AgentNodeRequest{Name: "n", Port: 8443}},
		{"an address with a path", AgentNodeRequest{Name: "n", Address: "node.example.com/x", Port: 8443}},
		{"port zero", AgentNodeRequest{Name: "n", Address: "node.example.com"}},
		{"a port past the range", AgentNodeRequest{Name: "n", Address: "node.example.com", Port: 70000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if pairing, err := (&NodeService{}).CreateAgent(&tt.req); err == nil {
				t.Fatalf("CreateAgent accepted %+v: %+v", tt.req, pairing)
			}
			var count int64
			if err := database.GetDB().Model(model.Node{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("%d nodes stored after a refused request, %v", count, err)
			}
		})
	}
}

func TestAFreshBundlePairsTheNodeWithARunningAgent(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "127.0.0.1", Port: 8443, AllowPrivateAddress: true})
	if err != nil {
		t.Fatal(err)
	}
	port, guid := serveIdleAgent(t, parsePairing(t, pairing))

	node := pointAt(t, pairing.Node.Id, port)
	patch, err := svc.Probe(context.Background(), node)
	if err != nil || patch.Guid != guid {
		t.Fatalf("Probe = %+v, %v; want the agent that holds the bundle, guid %s", patch, err, guid)
	}
}

func TestRepairAgentCutsOffTheAgentOfTheOldBundle(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	first, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "127.0.0.1", Port: 8443, AllowPrivateAddress: true})
	if err != nil {
		t.Fatal(err)
	}
	id := first.Node.Id
	oldPort, _ := serveIdleAgent(t, parsePairing(t, first))
	if _, err := svc.Probe(context.Background(), pointAt(t, id, oldPort)); err != nil {
		t.Fatalf("setup: the agent of the first bundle is not reachable: %v", err)
	}
	if err := svc.ClearNodeDirty(id, first.Node.ConfigDirtyAt); err != nil {
		t.Fatal(err)
	}

	second, err := svc.RepairAgent(id)
	if err != nil {
		t.Fatalf("RepairAgent: %v", err)
	}
	oldBundle, newBundle := parsePairing(t, first), parsePairing(t, second)
	if newBundle.Secret == oldBundle.Secret || second.Node.PinnedCertSha256 == first.Node.PinnedCertSha256 {
		t.Fatal("repairing kept the old secret or the old certificate")
	}
	if want := bundlePin(t, newBundle); second.Node.PinnedCertSha256 != want {
		t.Fatalf("node pins %q, the new bundle's certificate is %q", second.Node.PinnedCertSha256, want)
	}
	if newBundle.Address != second.Node.Address || newBundle.Port != second.Node.Port {
		t.Fatalf("new bundle names %s:%d, want the node's endpoint", newBundle.Address, newBundle.Port)
	}
	if !second.Node.ConfigDirty {
		t.Fatal("a re-paired node is not dirty, so its agent would wait for the periodic comparison")
	}

	if _, err := svc.Probe(context.Background(), pointAt(t, id, oldPort)); err == nil {
		t.Fatal("the agent of the old bundle still answers the node")
	}
	newPort, _ := serveIdleAgent(t, newBundle)
	if _, err := svc.Probe(context.Background(), pointAt(t, id, newPort)); err != nil {
		t.Fatalf("the agent of the new bundle is not reachable: %v", err)
	}
}

func TestRepairAgentRefusesWhatIsNotAnAgent(t *testing.T) {
	setupConflictDB(t)
	panel := &model.Node{Name: "panel", Scheme: "https", Address: "203.0.113.9", Port: 2053, BasePath: "/", ApiToken: "token", Enable: true}
	if err := database.GetDB().Create(panel).Error; err != nil {
		t.Fatal(err)
	}
	if pairing, err := (&NodeService{}).RepairAgent(panel.Id); err == nil {
		t.Fatalf("RepairAgent minted a bundle for a panel node: %+v", pairing)
	}
	stored, err := (&NodeService{}).GetById(panel.Id)
	if err != nil || stored.ApiToken != "token" {
		t.Fatalf("the refused repair touched the panel node: %+v, %v", stored, err)
	}
}

func TestUpdateFromRequestCannotChangeWhatTheBundleSets(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	id, pin := pairing.Node.Id, pairing.Node.PinnedCertSha256
	bundle := parsePairing(t, pairing)

	req := &NodeMutationRequest{
		Name: "renamed", Remark: "moved", Scheme: "http", Address: "203.0.113.8", Port: 9443, BasePath: "/x",
		Enable: true, TlsVerifyMode: "skip", InboundSyncMode: "selected", InboundTags: []string{"t"},
	}
	if err := svc.UpdateFromRequest(id, req); err != nil {
		t.Fatalf("UpdateFromRequest: %v", err)
	}
	got, err := svc.GetById(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" || got.Remark != "moved" || got.Address != "203.0.113.8" || got.Port != 9443 {
		t.Fatalf("node = %+v, want the name, remark and endpoint the request set", got)
	}
	if got.Kind != model.NodeKindAgent || got.Scheme != "https" || got.BasePath != "/" || got.TlsVerifyMode != "pin" ||
		got.PinnedCertSha256 != pin || got.InboundSyncMode != "all" || len(got.InboundTags) != 0 || got.ApiToken != bundle.Secret {
		t.Fatalf("node = %+v, want the transport, pin and secret of its bundle untouched", got)
	}

	replacement := "another-secret"
	for name, bad := range map[string]*NodeMutationRequest{
		"a new secret":  {Name: "n", Address: "203.0.113.8", Port: 9443, Enable: true, ApiToken: &replacement},
		"a cleared one": {Name: "n", Address: "203.0.113.8", Port: 9443, ClearApiToken: true},
	} {
		if err := svc.UpdateFromRequest(id, bad); err == nil || !strings.Contains(err.Error(), "pairing it again") {
			t.Fatalf("%s: UpdateFromRequest = %v, want a refusal that points to pairing", name, err)
		}
	}
	if again, err := svc.GetById(id); err != nil || again.ApiToken != bundle.Secret {
		t.Fatalf("a refused update changed the secret: %+v, %v", again, err)
	}
}

func TestRuntimeNodeFromRequestKeepsTheTransportOfAnAgent(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "127.0.0.1", Port: 8443, AllowPrivateAddress: true})
	if err != nil {
		t.Fatal(err)
	}
	port, guid := serveIdleAgent(t, parsePairing(t, pairing))

	n, err := svc.RuntimeNodeFromRequest(pairing.Node.Id, &NodeMutationRequest{
		Name: "edge", Scheme: "http", Address: "127.0.0.1", Port: port, BasePath: "/x", Enable: true,
		AllowPrivateAddress: true, TlsVerifyMode: "verify",
	})
	if err != nil {
		t.Fatalf("RuntimeNodeFromRequest: %v", err)
	}
	if n.Scheme != "https" || n.TlsVerifyMode != "pin" || n.PinnedCertSha256 != pairing.Node.PinnedCertSha256 {
		t.Fatalf("node = %+v, want the pin and transport the bundle set", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if patch, err := svc.Probe(ctx, n); err != nil || patch.Guid != guid {
		t.Fatalf("Probe of the overlaid node = %+v, %v; want the agent reached", patch, err)
	}
}

// The reachability check runs before the update, so it has to give the same answer: a secret
// that only pairing changes is refused, not probed into a decode or handshake error.
func TestRuntimeNodeFromRequestRefusesASecretForAnAgent(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}

	typo := "typo"
	for name, bad := range map[string]*NodeMutationRequest{
		"a new secret":  {Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, ApiToken: &typo},
		"a cleared one": {Name: "edge", Address: "203.0.113.7", Port: 8443, ClearApiToken: true},
	} {
		if n, err := svc.RuntimeNodeFromRequest(pairing.Node.Id, bad); err == nil || !strings.Contains(err.Error(), "pairing it again") {
			t.Fatalf("%s: RuntimeNodeFromRequest = %+v, %v; want a refusal that points to pairing", name, n, err)
		}
	}
}
