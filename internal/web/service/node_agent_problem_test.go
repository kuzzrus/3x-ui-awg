package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func setNodeTags(t *testing.T, nodeID int, tags string) {
	t.Helper()
	if err := database.GetDB().Model(model.Node{}).Where("id = ?", nodeID).Update("tags", tags).Error; err != nil {
		t.Fatal(err)
	}
}

func TestAgentSyncKeepsTheReasonItCannotBuildAConfigUntilItCan(t *testing.T) {
	f := newSyncAgentFixture(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}))
	addTemplateRules(t, markedRule("marker-us-only", map[string]any{
		"nodeTags": []any{"eu"}, "outboundTag": "us-hop",
	}))
	setNodeTags(t, f.nodeID, `["eu"]`)
	t.Cleanup(func() { noteAgentConfigProblem(f.nodeID, nil) })
	f.markDirty()

	svc := &AgentSyncService{}
	err := f.sync(svc, nil)
	if err == nil || !strings.Contains(err.Error(), `"us-hop"`) {
		t.Fatalf("Sync = %v, want a refusal that names the outbound the node lacks", err)
	}
	if problem := agentConfigProblem(f.nodeID); !strings.Contains(problem, `"us-hop"`) {
		t.Fatalf("problem = %q, want the reason kept for the heartbeat to show", problem)
	}
	if pushed := f.pushed(); len(pushed) != 0 {
		t.Fatalf("%d pushes went out for a config that could not be built", len(pushed))
	}

	setNodeTags(t, f.nodeID, `["eu","us"]`)
	f.markDirty()
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync once the node has the tag: %v", err)
	}
	if problem := agentConfigProblem(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the config was built, want it forgotten", problem)
	}
	if pushed := f.pushed(); len(pushed) != 1 {
		t.Fatalf("%d pushes, want the one config the node can have", len(pushed))
	}
}

func TestProbeAgentShowsWhyItsConfigCannotBeBuilt(t *testing.T) {
	node, _ := startIdleAgent(t)
	noteAgentConfigProblem(node.Id, errors.New(`a routing rule sends to outbound "us-hop"`))
	t.Cleanup(func() { noteAgentConfigProblem(node.Id, nil) })

	patch, err := (&NodeService{}).Probe(context.Background(), node)
	if err != nil {
		t.Fatalf("Probe: %v: an agent that answers is online, whatever its config", err)
	}
	if !strings.Contains(patch.LastError, `outbound "us-hop"`) {
		t.Fatalf("LastError = %q, want the reason the config cannot be built", patch.LastError)
	}

	noteAgentConfigProblem(node.Id, nil)
	patch, err = (&NodeService{}).Probe(context.Background(), node)
	if err != nil || patch.LastError != "" {
		t.Fatalf("Probe after the problem is gone = %q, %v, want no error", patch.LastError, err)
	}
}
