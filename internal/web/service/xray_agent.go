package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// GetAgentConfig builds the whole Xray config an agent node runs: the panel's template and
// subscription outbounds with the node's own enabled inbounds. The injections that only
// the master's host can serve (sidecar bridges, panel egress, node egresses) are left out.
func (s *XrayService) GetAgentConfig(nodeID int) (*xray.Config, error) {
	xrayConfig, err := s.newConfigFromTemplate()
	if err != nil {
		return nil, err
	}
	inbounds, err := s.inboundService.GetInboundsByNode(nodeID)
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable || !isNodeEligibleProtocol(inbound.Protocol) {
			continue
		}
		inboundConfig, err := s.renderInboundConfig(inbound)
		if err != nil {
			return nil, err
		}
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}
	mergeActiveSubscriptionOutbounds(xrayConfig)
	return xrayConfig, nil
}

// RenderAgentConfig is GetAgentConfig as the bytes the agent is sent and hashes.
func (s *XrayService) RenderAgentConfig(nodeID int) ([]byte, error) {
	xrayConfig, err := s.GetAgentConfig(nodeID)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(xrayConfig, "", "  ")
}
