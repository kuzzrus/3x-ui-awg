package service

import (
	"encoding/json"
	"fmt"
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

// missingOutboundTag is the blackhole that takes the traffic of a rule whose outbound the config lacks.
const missingOutboundTag = "x-ui-missing-outbound"

// onlyNodesTagged says who has an outbound that a config is missing.
func onlyNodesTagged(tags []string) string {
	if len(tags) == 0 {
		return ""
	}
	sorted := slices.Clone(tags)
	slices.SortFunc(sorted, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	return " (only the agent nodes tagged " + strings.Join(sorted, ", ") + " have it)"
}

func outboundTagSet(outbounds json_util.RawMessage) map[string]bool {
	var list []struct {
		Tag string `json:"tag"`
	}
	set := map[string]bool{}
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return set
	}
	for _, outbound := range list {
		set[outbound.Tag] = true
	}
	return set
}

// selectorEmptied reports whether a balancer selects outbounds that are gone and none that are left.
func selectorEmptied(selector any, have map[string]bool, gone map[string][]string) bool {
	entries, _ := selector.([]any)
	matches := func(tag string) bool {
		for _, entry := range entries {
			if prefix, ok := entry.(string); ok && strings.HasPrefix(tag, prefix) {
				return true
			}
		}
		return false
	}
	for tag := range have {
		if matches(tag) {
			return false
		}
	}
	for tag := range gone {
		if matches(tag) {
			return true
		}
	}
	return false
}

// pointLeftOutAtABlackhole repairs what scopeOutbounds left dangling and lists what it changed: a rule whose
// outbound or balancer is gone sends to a blackhole, and a fallback that is gone is dropped.
func pointLeftOutAtABlackhole(cfg *xray.Config, left map[string][]string) []string {
	if len(left) == 0 || len(cfg.RouterConfig) == 0 {
		return nil
	}
	have := outboundTagSet(cfg.OutboundConfigs)
	gone := map[string][]string{}
	for tag, tags := range left {
		if !have[tag] {
			gone[tag] = tags
		}
	}
	var routing map[string]any
	if len(gone) == 0 || json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return nil
	}

	var notes []string
	emptied := map[string]bool{}
	balancers, _ := routing["balancers"].([]any)
	for _, raw := range balancers {
		balancer, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := balancer["tag"].(string)
		if fallback, _ := balancer["fallbackTag"].(string); fallback != "" {
			if tags, isGone := gone[fallback]; isGone {
				delete(balancer, "fallbackTag")
				notes = append(notes, fmt.Sprintf("balancer %q falls back to outbound %q, which this config does not have%s: the fallback is dropped",
					tag, fallback, onlyNodesTagged(tags)))
			}
		}
		if selectorEmptied(balancer["selector"], have, gone) {
			emptied[tag] = true
		}
	}

	blocked := false
	rules, _ := routing["rules"].([]any)
	for _, raw := range rules {
		rule, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if target, _ := rule["outboundTag"].(string); target != "" {
			if tags, isGone := gone[target]; isGone {
				rule["outboundTag"] = missingOutboundTag
				blocked = true
				notes = append(notes, fmt.Sprintf("a routing rule sends to outbound %q, which this config does not have%s: its traffic is blocked",
					target, onlyNodesTagged(tags)))
			}
		}
		if target, _ := rule["balancerTag"].(string); target != "" && emptied[target] {
			delete(rule, "balancerTag")
			rule["outboundTag"] = missingOutboundTag
			blocked = true
			notes = append(notes, fmt.Sprintf("a routing rule sends to balancer %q, whose outbounds this config does not have: its traffic is blocked", target))
		}
	}
	if len(notes) == 0 {
		return nil
	}

	rewritten, err := json.Marshal(routing)
	if err != nil {
		return nil
	}
	if blocked {
		var list []json.RawMessage
		if len(cfg.OutboundConfigs) > 0 && json.Unmarshal(cfg.OutboundConfigs, &list) != nil {
			return nil
		}
		list = append(list, json.RawMessage(`{"protocol":"blackhole","tag":"`+missingOutboundTag+`"}`))
		outbounds, err := json.Marshal(list)
		if err != nil {
			return nil
		}
		cfg.OutboundConfigs = outbounds
	}
	cfg.RouterConfig = rewritten
	return notes
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
