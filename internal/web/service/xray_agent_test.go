package service

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func seedAgentRow(t *testing.T, name string) int {
	t.Helper()
	node := &model.Node{Name: name, Kind: model.NodeKindAgent, Address: "192.0.2.10", Port: 8443, Enable: true}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return node.Id
}

func seedInboundOn(t *testing.T, nodeID *int, tag string, protocol model.Protocol, enable bool, clients []model.Client) *model.Inbound {
	t.Helper()
	in := &model.Inbound{
		Tag:      tag,
		Enable:   enable,
		Port:     40000 + len(tag),
		Protocol: protocol,
		Settings: `{"clients":[],"decryption":"none"}`,
		NodeID:   nodeID,
	}
	if err := database.GetDB().Create(in).Error; err != nil {
		t.Fatalf("create inbound %s: %v", tag, err)
	}
	if err := (&ClientService{}).SyncInbound(nil, in.Id, clients); err != nil {
		t.Fatalf("SyncInbound %s: %v", tag, err)
	}
	return in
}

func inboundTags(cfg *xray.Config) []string {
	tags := make([]string, 0, len(cfg.InboundConfigs))
	for _, in := range cfg.InboundConfigs {
		tags = append(tags, in.Tag)
	}
	slices.Sort(tags)
	return tags
}

func TestGetAgentConfigHoldsOnlyTheNodesOwnInbounds(t *testing.T) {
	setupSettingTestDB(t)
	mine, other := seedAgentRow(t, "mine"), seedAgentRow(t, "other")
	live := []model.Client{
		{Email: "live@x", ID: "11111111-1111-1111-1111-111111111111", Enable: true},
		{Email: "off@x", ID: "22222222-2222-2222-2222-222222222222", Enable: true},
	}
	seedInboundOn(t, &mine, "n1-vless-a", model.VLESS, true, live)
	seedInboundOn(t, &mine, "n1-vless-off", model.VLESS, false, nil)
	seedInboundOn(t, &other, "n2-vless-b", model.VLESS, true, nil)
	seedInboundOn(t, nil, "local-vless", model.VLESS, true, nil)
	seedInboundOn(t, &mine, "n1-mtproto", model.MTProto, true, nil)
	disableClients(t, "off@x")

	cfg, err := (&XrayService{}).GetAgentConfig(mine)
	if err != nil {
		t.Fatalf("GetAgentConfig: %v", err)
	}
	if got, want := inboundTags(cfg), []string{"api", "n1-vless-a"}; !slices.Equal(got, want) {
		t.Fatalf("inbounds = %v, want the template's api inbound and the node's one enabled, eligible inbound: %v", got, want)
	}

	var emitted map[string]any
	for _, in := range cfg.InboundConfigs {
		if in.Tag == "n1-vless-a" {
			if err := json.Unmarshal([]byte(in.Settings), &emitted); err != nil {
				t.Fatal(err)
			}
		}
	}
	clients, _ := emitted["clients"].([]any)
	if len(clients) != 1 || clients[0].(map[string]any)["email"] != "live@x" {
		t.Fatalf("clients = %#v, want only the enabled one", emitted["clients"])
	}
}

func TestGetAgentConfigLeavesOutWhatOnlyTheMasterCanServe(t *testing.T) {
	setupSettingTestDB(t)
	node := seedAgentRow(t, "agent")
	if err := (&SettingService{}).SetPanelOutbound("direct"); err != nil {
		t.Fatal(err)
	}

	svc := &XrayService{}
	local, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(inboundTags(local), PanelEgressInboundTag) {
		t.Fatalf("setup: the master's own config has no %s inbound: %v", PanelEgressInboundTag, inboundTags(local))
	}

	agent, err := svc.GetAgentConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	if got := inboundTags(agent); slices.Contains(got, PanelEgressInboundTag) {
		t.Fatalf("the agent's config carries the master's panel egress: %v", got)
	}
}

func TestRenderAgentConfigIsTheSameBytesEveryTime(t *testing.T) {
	setupSettingTestDB(t)
	node := seedAgentRow(t, "agent")
	for i, email := range []string{"a@x", "b@x", "c@x"} {
		seedInboundOn(t, &node, "n1-in-"+email, model.VLESS, true, []model.Client{
			{Email: email, ID: "3333333" + string(rune('0'+i)) + "-3333-3333-3333-333333333333", Enable: true},
		})
	}

	svc := &XrayService{}
	first, err := svc.RenderAgentConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		again, err := svc.RenderAgentConfig(node)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, again) {
			t.Fatal("rendering the same state twice gave different bytes, so the master would push on every tick")
		}
	}
}
