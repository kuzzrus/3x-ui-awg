package service

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// scopeOutbounds drops the outbounds whose nodeTags keep them out of this scope and strips the panel-only
// nodeTags key from the rest. It returns the tag of each it dropped with the nodeTags that would give it.
func scopeOutbounds(outbounds json_util.RawMessage, scope configScope) (json_util.RawMessage, map[string][]string) {
	var list []json.RawMessage
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return outbounds, nil
	}
	kept := make([]json.RawMessage, 0, len(list))
	var dropped map[string][]string
	changed := false
	for _, raw := range list {
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields["nodeTags"] == nil {
			kept = append(kept, raw)
			continue
		}
		changed = true
		var rawTags any
		_ = json.Unmarshal(fields["nodeTags"], &rawTags)
		if !nodeTagsFit(rawTags, scope) {
			var tag string
			_ = json.Unmarshal(fields["tag"], &tag)
			if tag != "" {
				if dropped == nil {
					dropped = map[string][]string{}
				}
				dropped[tag], _ = ruleNodeTags(rawTags)
			}
			continue
		}
		delete(fields, "nodeTags")
		stripped, err := json.Marshal(fields)
		if err != nil {
			kept = append(kept, raw)
			continue
		}
		kept = append(kept, stripped)
	}
	if !changed {
		return outbounds, nil
	}
	out, err := json.Marshal(kept)
	if err != nil {
		logger.Warning("outbounds with nodeTags could not be written back, all kept:", err)
		return outbounds, nil
	}
	return out, dropped
}

// checkRulesReachTheirOutbounds refuses a config in which a rule sends to an outbound that scopeOutbounds
// dropped. Xray takes such a rule for a typo and sends its traffic out of the default outbound.
func checkRulesReachTheirOutbounds(cfg *xray.Config, left map[string][]string) error {
	if len(left) == 0 || len(cfg.RouterConfig) == 0 {
		return nil
	}
	var routing struct {
		Rules []struct {
			OutboundTag string `json:"outboundTag"`
		} `json:"rules"`
	}
	if json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return nil
	}
	for _, rule := range routing.Rules {
		tags, gone := left[rule.OutboundTag]
		if !gone || outboundTagExists(cfg.OutboundConfigs, rule.OutboundTag) {
			continue
		}
		if len(tags) == 0 {
			return common.NewErrorf("a routing rule sends to outbound %q, which no node has", rule.OutboundTag)
		}
		return common.NewErrorf("a routing rule sends to outbound %q, which this node does not have: only the agent nodes tagged %s do",
			rule.OutboundTag, strings.Join(tags, ", "))
	}
	return nil
}

// checkOutboundNodeTags refuses an outbound whose nodeTags are not a list of tags.
func checkOutboundNodeTags(outbounds json_util.RawMessage) error {
	var list []map[string]any
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return nil
	}
	for i, outbound := range list {
		raw, ok := outbound["nodeTags"]
		if !ok {
			continue
		}
		if _, ok := ruleNodeTags(raw); !ok {
			return common.NewErrorf("xray template config invalid: outbound %d: nodeTags must be a list of tags of up to %d characters", i+1, maxNodeTagLen)
		}
	}
	return nil
}

// checkSharedRulesReachTheirOutbounds refuses a rule that is for every core but sends to an outbound
// only some agent nodes have, which the panel's own core would be left without.
func checkSharedRulesReachTheirOutbounds(routerCfg, outbounds json_util.RawMessage) error {
	var list []map[string]any
	if len(routerCfg) == 0 || len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return nil
	}
	nodeOnly := map[string][]string{}
	for _, outbound := range list {
		tag, _ := outbound["tag"].(string)
		tags, ok := ruleNodeTags(outbound["nodeTags"])
		if tag != "" && ok && len(tags) > 0 {
			nodeOnly[tag] = tags
		}
	}
	if len(nodeOnly) == 0 {
		return nil
	}
	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	if json.Unmarshal(routerCfg, &routing) != nil {
		return nil
	}
	for i, rule := range routing.Rules {
		if enabled, ok := rule["enabled"].(bool); ok && !enabled && !isApiRule(rule) {
			continue
		}
		if tags, ok := ruleNodeTags(rule["nodeTags"]); ok && len(tags) > 0 {
			continue
		}
		target, _ := rule["outboundTag"].(string)
		if tags, only := nodeOnly[target]; only {
			slices.SortFunc(tags, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
			return common.NewErrorf("xray template config invalid: routing rule %d is for every core but sends to outbound %q, which only the agent nodes tagged %s have",
				i+1, target, strings.Join(tags, ", "))
		}
	}
	return nil
}
