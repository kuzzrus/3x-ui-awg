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

func TestAgentSyncPushesTheAdjustedConfigAndKeepsWhatItAdjusted(t *testing.T) {
	f := newSyncAgentFixture(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}))
	addTemplateRules(t, markedRule("marker-us-only", map[string]any{
		"nodeTags": []any{"eu"}, "outboundTag": "us-hop",
	}))
	setNodeTags(t, f.nodeID, `["eu"]`)
	t.Cleanup(func() { setAgentConfigProblem(f.nodeID, "") })
	f.markDirty()

	svc := &AgentSyncService{}
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if pushed := f.pushed(); len(pushed) != 1 || !strings.Contains(string(pushed[0].body), missingOutboundTag) {
		t.Fatalf("pushes = %d, want the whole config, with the traffic of the rule blocked, so that nothing else waits for the tags", len(pushed))
	}
	if problem := agentConfigProblem(f.nodeID); !strings.Contains(problem, `"us-hop"`) {
		t.Fatalf("problem = %q, want the reason kept for the heartbeat to show", problem)
	}

	setNodeTags(t, f.nodeID, `["eu","us"]`)
	f.markDirty()
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync once the node has the tag: %v", err)
	}
	if problem := agentConfigProblem(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the node got the tag, want it forgotten", problem)
	}
	if pushed := f.pushed(); len(pushed) != 2 || strings.Contains(string(pushed[1].body), missingOutboundTag) {
		t.Fatalf("pushes = %d, want a second config that sends the rule to us-hop", len(pushed))
	}
}

func TestAgentSyncKeepsTheReasonItCannotBuildAConfig(t *testing.T) {
	f := newSyncAgentFixture(t)
	t.Cleanup(func() { setAgentConfigProblem(f.nodeID, "") })
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "{not json"); err != nil {
		t.Fatal(err)
	}
	f.markDirty()

	svc := &AgentSyncService{}
	err := f.sync(svc, nil)
	if err == nil {
		t.Fatal("Sync built a config out of a template that is no JSON")
	}
	if problem := agentConfigProblem(f.nodeID); !strings.HasPrefix(problem, "cannot build the config of this node:") {
		t.Fatalf("problem = %q, want the reason the config cannot be built", problem)
	}
	if pushed := f.pushed(); len(pushed) != 0 {
		t.Fatalf("%d pushes went out for a config that could not be built", len(pushed))
	}
}

func TestProbeAgentShowsWhyItsConfigCannotBeBuilt(t *testing.T) {
	node, _ := startIdleAgent(t)
	setAgentConfigProblem(node.Id, unbuildableConfigProblem(errors.New(`a routing rule sends to outbound "us-hop"`)))
	t.Cleanup(func() { setAgentConfigProblem(node.Id, "") })

	patch, err := (&NodeService{}).Probe(context.Background(), node)
	if err != nil {
		t.Fatalf("Probe: %v: an agent that answers is online, whatever its config", err)
	}
	if !strings.Contains(patch.LastError, `outbound "us-hop"`) {
		t.Fatalf("LastError = %q, want the reason the config cannot be built", patch.LastError)
	}

	setAgentConfigProblem(node.Id, "")
	patch, err = (&NodeService{}).Probe(context.Background(), node)
	if err != nil || patch.LastError != "" {
		t.Fatalf("Probe after the problem is gone = %q, %v, want no error", patch.LastError, err)
	}
}
