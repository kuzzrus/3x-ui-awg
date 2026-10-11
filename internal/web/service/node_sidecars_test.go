package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestAgentNodeSidecarsAreSavedAndReplaced(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Sidecars: []string{" tor", "tor"}})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	if want := torOnly(); !slices.Equal(pairing.Node.Sidecars, want) {
		t.Fatalf("the new agent's helper programs = %q, want %q", pairing.Node.Sidecars, want)
	}

	save := func(sidecars []string) []string {
		t.Helper()
		req := &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, Sidecars: sidecars}
		if err := svc.UpdateFromRequest(id, req); err != nil {
			t.Fatalf("UpdateFromRequest: %v", err)
		}
		view, err := svc.GetViewById(id)
		if err != nil {
			t.Fatal(err)
		}
		return view.Sidecars
	}
	if got := save(torOnly()); !slices.Equal(got, torOnly()) {
		t.Fatalf("a save that names tor left %q", got)
	}
	// A form-encoded request cannot say an empty list, so none named is the way to stop them.
	if got := save(nil); got == nil || len(got) != 0 {
		t.Fatalf("a save that names none left %#v, want none, as an empty list", got)
	}
	err = svc.UpdateFromRequest(id, &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, Sidecars: []string{"no-such-program"}})
	if err == nil || !strings.Contains(err.Error(), `"no-such-program"`) {
		t.Fatalf("a program no agent runs was accepted: %v", err)
	}
	if _, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge2", Address: "203.0.113.8", Port: 8443, Sidecars: []string{"nope"}}); err == nil {
		t.Fatal("an agent was added with a helper program no agent runs")
	}
}

func TestSavingTheHelperProgramsMarksTheNodeDirty(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	if err := svc.ClearNodeDirty(id, pairing.Node.ConfigDirtyAt); err != nil {
		t.Fatal(err)
	}
	if err := svc.UpdateFromRequest(id, &NodeMutationRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Enable: true, Sidecars: torOnly()}); err != nil {
		t.Fatal(err)
	}
	node, err := svc.GetById(id)
	if err != nil {
		t.Fatal(err)
	}
	if !node.ConfigDirty {
		t.Fatal("a save left the node clean, so the tick that sends the list waits for the periodic check")
	}
}

func TestOnlyAnAgentNodeRunsHelperPrograms(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	token := "token"
	view, err := svc.CreateFromRequest(&NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, ApiToken: &token, Sidecars: torOnly(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.Sidecars == nil || len(view.Sidecars) != 0 {
		t.Fatalf("a stock panel node came back with helper programs %#v", view.Sidecars)
	}
	if err := svc.UpdateFromRequest(view.Id, &NodeMutationRequest{
		Name: "stock", Address: "203.0.113.9", Port: 2053, Enable: true, Sidecars: torOnly(),
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := svc.GetViewById(view.Id)
	if err != nil || len(stored.Sidecars) != 0 {
		t.Fatalf("a stock panel node holds helper programs %q, %v; it has no agent to run them", stored.Sidecars, err)
	}
}

// A row written before the columns existed holds NULL there, and it must load like a node with none.
func TestAgentRowFromBeforeTheSidecarColumnsReadsAsNone(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Sidecars: torOnly()})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	if err := database.GetDB().Exec("UPDATE nodes SET sidecars = NULL, sidecar_states = NULL WHERE id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetViewById(id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Sidecars == nil || len(view.Sidecars) != 0 || view.SidecarStates == nil || len(view.SidecarStates) != 0 {
		t.Fatalf("a row with NULL columns reads as %#v and %#v, want empty lists the page can map over", view.Sidecars, view.SidecarStates)
	}
}

func TestHeartbeatStoresHowTheHelperProgramsStand(t *testing.T) {
	setupConflictDB(t)
	svc := &NodeService{}
	pairing, err := svc.CreateAgent(&AgentNodeRequest{Name: "edge", Address: "203.0.113.7", Port: 8443, Sidecars: torOnly()})
	if err != nil {
		t.Fatal(err)
	}
	id := pairing.Node.Id
	states := []model.NodeSidecar{{Name: "tor", State: "running", Message: "Bootstrapped 100% (done): Done"}}

	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "online", SidecarStates: states}); err != nil {
		t.Fatal(err)
	}
	view, err := svc.GetViewById(id)
	if err != nil || !slices.Equal(view.SidecarStates, states) {
		t.Fatalf("states = %+v, %v; want what the heartbeat carried", view.SidecarStates, err)
	}

	// A probe that failed, or a node that does not report them, says nothing about the programs.
	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "offline", LastError: "unreachable"}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.GetViewById(id); !slices.Equal(view.SidecarStates, states) {
		t.Fatalf("states = %+v after an unreachable probe, want the last known ones", view.SidecarStates)
	}

	// An agent that runs none reports an empty list, which clears what was there.
	if err := svc.UpdateHeartbeat(id, HeartbeatPatch{Status: "online", SidecarStates: []model.NodeSidecar{}}); err != nil {
		t.Fatal(err)
	}
	if view, _ := svc.GetViewById(id); len(view.SidecarStates) != 0 {
		t.Fatalf("states = %+v, want none once the agent reports none", view.SidecarStates)
	}
}

func TestSidecarStatesFromAnAgentAreBounded(t *testing.T) {
	long := strings.Repeat("я", maxSidecarMessageLen*2)
	got := sidecarStatesFrom([]agentproto.SidecarState{
		{Name: "tor", State: "running", Message: long},
		{Name: "tor", State: "failed", Message: "a repeat"},
		{Name: "unknown", State: "running"},
		{Name: "", State: "running"},
	})
	if len(got) != 1 || got[0].Name != "tor" || got[0].State != "running" || len([]rune(got[0].Message)) != maxSidecarMessageLen {
		t.Fatalf("states = %+v, want tor once, its message cut to %d characters, and nothing for what the panel does not know", got, maxSidecarMessageLen)
	}
	if states := sidecarStatesFrom(nil); states == nil || len(states) != 0 {
		t.Fatalf("states of an agent that reports none = %#v, want an empty list, which the heartbeat stores as such", states)
	}
}
