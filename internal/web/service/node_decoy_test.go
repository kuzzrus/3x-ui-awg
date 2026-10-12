package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func decoyForm(mode, domain string) NodeDecoyForm {
	return NodeDecoyForm{DecoyMode: mode, DecoyDomain: domain}
}

func TestAgentNodeDecoyIsSavedNormalizedAndReplaced(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, NodeDecoyForm: decoyForm("template", " Node.Example. ")})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	want := model.NodeDecoy{Mode: "template", Template: "maintenance", Domain: "node.example", Cert: "auto", Port: 7443}
	if pairing.Node.Decoy != want {
		t.Fatalf("the new agent's decoy = %+v, want %+v", pairing.Node.Decoy, want)
	}

	save := func(form NodeDecoyForm) (model.NodeDecoy, error) {
		t.Helper()
		req := &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, NodeDecoyForm: form}
		if err := svc.UpdateFromRequest(id, req); err != nil {
			return model.NodeDecoy{}, err
		}
		view, err := svc.GetViewById(id)
		if err != nil {
			t.Fatal(err)
		}
		return view.Decoy, nil
	}
	proxy := NodeDecoyForm{DecoyMode: "proxy", DecoyProxyURL: "https://site.example", DecoyDomain: "node.example", DecoyCert: "manual", DecoyCertFile: "/c.pem", DecoyKeyFile: "/k.pem", DecoyPort: 7500}
	got, err := save(proxy)
	if err != nil || got != (model.NodeDecoy{Mode: "proxy", ProxyURL: "https://site.example", Domain: "node.example", Cert: "manual", CertFile: "/c.pem", KeyFile: "/k.pem", Port: 7500}) {
		t.Fatalf("a save that names a decoy left %+v, %v", got, err)
	}

	// A refused decoy changes nothing, and neither does it save the rest of the node half way.
	for name, form := range map[string]NodeDecoyForm{
		"a page the panel does not have": {DecoyMode: "template", DecoyTemplate: "no-such-page", DecoyDomain: "node.example"},
		"no domain":                      {DecoyMode: "template"},
		"a mode nobody serves":           {DecoyMode: "upload", DecoyDomain: "node.example"},
	} {
		if _, err := save(form); err == nil {
			t.Errorf("%s: the save was taken", name)
		}
	}
	if view, _ := svc.GetViewById(id); view.Decoy.Mode != "proxy" {
		t.Fatalf("decoy after refusals = %+v, want the one from before", view.Decoy)
	}

	// A form-encoded request cannot say an empty decoy, so none named is the way to take it off.
	if got, err := save(NodeDecoyForm{}); err != nil || got != (model.NodeDecoy{}) {
		t.Fatalf("a save that names none left %+v, %v, want none", got, err)
	}
	if _, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge2", Address: "203.0.113.8", Port: 8443, NodeDecoyForm: decoyForm("template", "")}); err == nil || !strings.Contains(err.Error(), "domain") {
		t.Fatalf("an agent was added with a decoy that has no domain: %v", err)
	}
}

func TestOnlyAnAgentNodeServesADecoy(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	token := "token"
	view, err := svc.CreateFromRequest(&NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, ApiToken: &token, NodeDecoyForm: decoyForm("template", "stock.example"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Decoy != (model.NodeDecoy{}) {
		t.Fatalf("a stock panel node came back with the decoy %+v", view.Decoy)
	}
	if err := svc.UpdateFromRequest(view.Id, &NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, NodeDecoyForm: decoyForm("template", "stock.example"),
	}); err != nil {
		t.Fatal(err)
	}
	if stored, err := svc.GetViewById(view.Id); err != nil || stored.Decoy != (model.NodeDecoy{}) {
		t.Fatalf("a stock panel node holds the decoy %+v, %v; it has no agent to serve it", stored.Decoy, err)
	}
}

// A row written before the columns existed holds NULL there, and it must load like a node with no decoy.
func TestAgentRowFromBeforeTheDecoyColumnsReadsAsNone(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, NodeDecoyForm: decoyForm("template", "node.example")})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	if err := database.GetDB().Exec("UPDATE nodes SET decoy = NULL, decoy_state = NULL WHERE id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetViewById(id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Decoy != (model.NodeDecoy{}) || view.DecoyState != (model.NodeDecoyState{}) {
		t.Fatalf("a row with NULL columns reads as %+v and %+v, want none", view.Decoy, view.DecoyState)
	}
}

func TestHeartbeatStoresHowTheDecoyStands(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, NodeDecoyForm: decoyForm("template", "node.example")})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	state := model.NodeDecoyState{State: "running", Cert: "obtained", CertNotAfter: 1_800_000_000}

	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "online", DecoyState: &state}); err != nil {
		t.Fatal(err)
	}
	if view, err := svc.GetViewById(id); err != nil || view.DecoyState != state {
		t.Fatalf("state = %+v, %v; want what the heartbeat carried", view.DecoyState, err)
	}

	// A probe that failed says nothing about the decoy, and one that reports none (a stopped decoy is
	// still a state, "off") is not a probe of a node that reports them.
	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "offline", LastError: "unreachable"}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.GetViewById(id); view.DecoyState != state {
		t.Fatalf("state = %+v after an unreachable probe, want the last known one", view.DecoyState)
	}
	off := model.NodeDecoyState{State: "off"}
	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "online", DecoyState: &off}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.GetViewById(id); view.DecoyState != off {
		t.Fatalf("state = %+v, want what the agent reports now", view.DecoyState)
	}
}

func TestDecoyStateFromAnAgentIsBounded(t *testing.T) {
	long := strings.Repeat("я", maxSidecarMessageLen*2)
	got := decoyStateFrom(agentproto.DecoyState{State: strings.Repeat("s", 100), Cert: strings.Repeat("c", 100), CertNotAfter: 7, Message: long})
	if len(got.State) != maxDecoyStateLen || len(got.Cert) != maxDecoyStateLen || len([]rune(got.Message)) != maxSidecarMessageLen || got.CertNotAfter != 7 {
		t.Fatalf("state = %+v, want the texts cut to what the panel stores and the date kept", got)
	}
}
