package service

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// GetAgentConfig is the whole Xray config an agent node runs: the panel's template and subscription
// outbounds with the node's own enabled inbounds, and none of the injections only the master can serve.
func (s *XrayService) GetAgentConfig(nodeID int) (*xray.Config, error) {
	xrayConfig, err := s.newConfigFromTemplate()
	if err != nil {
		return nil, err
	}
	if err := inertAmneziaWGOutbounds(xrayConfig); err != nil {
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

// inertAmneziaWGOutbounds swaps each amneziawg outbound for a same-tag blackhole: the master's socks
// bridge needs a sidecar an agent lacks and a password that is new with every master process.
func inertAmneziaWGOutbounds(cfg *xray.Config) error {
	if len(cfg.OutboundConfigs) == 0 {
		return nil
	}
	var outbounds []json.RawMessage
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		return err
	}
	changed := false
	for i, raw := range outbounds {
		if !amneziawg.IsAmneziaWGOutbound(raw) {
			continue
		}
		var probe struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || probe.Tag == "" {
			return fmt.Errorf("amneziawg outbound %d: tag must be a non-empty string", i)
		}
		replacement, err := json.Marshal(map[string]string{"protocol": "blackhole", "tag": probe.Tag})
		if err != nil {
			return err
		}
		outbounds[i] = replacement
		changed = true
	}
	if !changed {
		return nil
	}
	raw, err := json.Marshal(outbounds)
	if err != nil {
		return err
	}
	cfg.OutboundConfigs = json_util.RawMessage(raw)
	return nil
}
