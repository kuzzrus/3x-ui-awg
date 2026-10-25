package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// An agent runs Xray alone, so the sidecar protocols that a panel node hosts are refused for it
// rather than stored and never served.
func TestCheckNodeCanHostProtocol_AgentRunsXrayAlone(t *testing.T) {
	setupConflictDB(t)
	agent := &model.Node{Name: "agent-a", Address: "127.0.0.1", Port: 2098, Kind: model.NodeKindAgent, PanelVersion: "v3.10.2"}
	panelNode := &model.Node{Name: "panel-a", Address: "127.0.0.1", Port: 2099, PanelVersion: "v3.10.2"}
	for _, n := range []*model.Node{agent, panelNode} {
		if err := database.GetDB().Create(n).Error; err != nil {
			t.Fatalf("create node %s: %v", n.Name, err)
		}
	}

	for _, p := range []model.Protocol{model.MTProto, model.AmneziaWG, model.TUIC, model.MASQUE} {
		t.Run(string(p), func(t *testing.T) {
			if err := checkNodeCanHostProtocol(database.GetDB(), agent.Id, p); err == nil ||
				!strings.Contains(err.Error(), "agent node") {
				t.Errorf("agent node accepted %s: err = %v", p, err)
			}
			if err := checkNodeCanHostProtocol(database.GetDB(), panelNode.Id, p); err != nil {
				t.Errorf("panel node refused %s: %v", p, err)
			}
		})
	}
	for _, p := range []model.Protocol{model.VLESS, model.VMESS, model.Trojan, model.Shadowsocks, model.Hysteria, model.WireGuard} {
		t.Run(string(p), func(t *testing.T) {
			for _, n := range []*model.Node{agent, panelNode} {
				if err := checkNodeCanHostProtocol(database.GetDB(), n.Id, p); err != nil {
					t.Errorf("node %s refused %s: %v", n.Name, p, err)
				}
			}
		})
	}
}

// A protocol that a node can host starts off an agent until it is added to the agent set on purpose.
func TestAgentEligibleProtocolsAreTheXrayOnesOfTheNodeSet(t *testing.T) {
	for p := range nodeEligibleProtocols {
		_, versioned := nodeProtocolFirstRelease[p]
		if isAgentEligibleProtocol(p) == versioned {
			t.Errorf("%s: agent eligible = %v, but the node set tracks a first release for it = %v", p, isAgentEligibleProtocol(p), versioned)
		}
	}
	for p := range agentEligibleProtocols {
		if !nodeEligibleProtocols[p] {
			t.Errorf("%s is agent eligible but not node eligible", p)
		}
	}
}
