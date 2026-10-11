package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
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

// chainsThroughGone lists the outbounds that are chained through one the config lacks. Their dials fail, which
// is as closed as it gets, so nothing is changed and the node only says so.
func chainsThroughGone(outbounds json_util.RawMessage, gone map[string][]string) []string {
	var list []struct {
		Tag            string `json:"tag"`
		StreamSettings struct {
			Sockopt struct {
				DialerProxy string `json:"dialerProxy"`
			} `json:"sockopt"`
		} `json:"streamSettings"`
	}
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return nil
	}
	var notes []string
	for _, outbound := range list {
		via := outbound.StreamSettings.Sockopt.DialerProxy
		if tags, isGone := gone[via]; via != "" && isGone {
			notes = append(notes, fmt.Sprintf("outbound %q is chained through outbound %q, which this config does not have%s: its connections fail",
				outbound.Tag, via, onlyNodesTagged(tags)))
		}
	}
	return notes
}

// repairLeftOut repairs what scopeOutbounds left dangling and lists what it found: a rule whose outbound or
// balancer is gone sends to a blackhole, a fallback that is gone is dropped, and a chain through one is reported.
func repairLeftOut(cfg *xray.Config, left map[string][]string) []string {
	if len(left) == 0 {
		return nil
	}
	have := outboundTagSet(cfg.OutboundConfigs)
	gone := map[string][]string{}
	for tag, tags := range left {
		if !have[tag] {
			gone[tag] = tags
		}
	}
	if len(gone) == 0 {
		return nil
	}
	return append(chainsThroughGone(cfg.OutboundConfigs, gone), repairRouting(cfg, have, gone)...)
}

// repairRouting is the part of repairLeftOut that changes the routing section.
func repairRouting(cfg *xray.Config, have map[string]bool, gone map[string][]string) []string {
	var routing map[string]any
	if len(cfg.RouterConfig) == 0 || json.Unmarshal(cfg.RouterConfig, &routing) != nil {
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

// checkOutboundNodeTags refuses an outbound whose nodeTags are not a list of tags, and the first one that has any:
// it is the default outbound of every core, and what the tags took away would change it.
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
		tags, ok := ruleNodeTags(raw)
		if !ok {
			return common.NewErrorf("xray template config invalid: outbound %d: nodeTags must be a list of tags of up to %d characters", i+1, maxNodeTagLen)
		}
		if i == 0 && len(tags) > 0 {
			return common.NewErrorf("xray template config invalid: outbound 1 is the default outbound of every core and cannot be limited to agent nodes: " +
				"move it down, or send the traffic of those nodes to it with a rule that carries their nodeTags")
		}
	}
	return nil
}

// nodeOnlyOutbounds maps the tag of each outbound that carries nodeTags to them.
func nodeOnlyOutbounds(list []map[string]any) map[string][]string {
	nodeOnly := map[string][]string{}
	for _, outbound := range list {
		tag, _ := outbound["tag"].(string)
		tags, ok := ruleNodeTags(outbound["nodeTags"])
		if tag != "" && ok && len(tags) > 0 {
			nodeOnly[tag] = tags
		}
	}
	return nodeOnly
}

// checkSharedEntriesReachTheirOutbounds refuses a rule or an outbound that is for every core but sends to,
// or is chained through, an outbound only some agent nodes have, which the panel's own core would be left without.
func checkSharedEntriesReachTheirOutbounds(routerCfg, outbounds json_util.RawMessage) error {
	var list []map[string]any
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil {
		return nil
	}
	nodeOnly := nodeOnlyOutbounds(list)
	if len(nodeOnly) == 0 {
		return nil
	}
	for i, outbound := range list {
		if tags, ok := ruleNodeTags(outbound["nodeTags"]); ok && len(tags) > 0 {
			continue
		}
		stream, _ := outbound["streamSettings"].(map[string]any)
		sockopt, _ := stream["sockopt"].(map[string]any)
		via, _ := sockopt["dialerProxy"].(string)
		if tags, only := nodeOnly[via]; via != "" && only {
			slices.SortFunc(tags, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
			return common.NewErrorf("xray template config invalid: outbound %d is for every core but is chained through outbound %q, which only the agent nodes tagged %s have",
				i+1, via, strings.Join(tags, ", "))
		}
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

// panelOutboundUses is what the panel's own core needs outbounds for, by the tag of the outbound: the panel
// outbound, the outbound a node is reached through and the outbound an inbound's egress bridge goes to.
func panelOutboundUses(settings *SettingService) map[string][]string {
	uses := map[string][]string{}
	if tag, err := settings.GetPanelOutbound(); err == nil && tag != "" {
		uses[tag] = append(uses[tag], "the panel outbound")
	}
	db := database.GetDB()
	var nodes []model.Node
	if err := db.Select("name", "outbound_tag").Where("outbound_tag <> '' AND enable = ?", true).Find(&nodes).Error; err == nil {
		for _, n := range nodes {
			uses[n.OutboundTag] = append(uses[n.OutboundTag], fmt.Sprintf("the node %q", n.Name))
		}
	}
	var inbounds []model.Inbound
	egress := []model.Protocol{model.MTProto, model.NaiveProxy, model.Tproxy}
	if err := db.Select("tag", "settings").Where("node_id IS NULL AND enable = ? AND protocol IN ?", true, egress).Find(&inbounds).Error; err == nil {
		for _, in := range inbounds {
			var parsed struct {
				RouteThroughXray bool   `json:"routeThroughXray"`
				OutboundTag      string `json:"outboundTag"`
			}
			if json.Unmarshal([]byte(in.Settings), &parsed) == nil && parsed.RouteThroughXray && parsed.OutboundTag != "" {
				uses[parsed.OutboundTag] = append(uses[parsed.OutboundTag], fmt.Sprintf("the inbound %q", in.Tag))
			}
		}
	}
	return uses
}

// checkPanelUsesKeepTheirOutbounds refuses to limit to agent nodes an outbound that the panel's own core is
// asked to use: the core would lack it, and the panel would go on with a direct connection and only a log line.
func checkPanelUsesKeepTheirOutbounds(settings *XraySettingService, outbounds json_util.RawMessage) error {
	var list []map[string]any
	if len(outbounds) == 0 || json.Unmarshal(outbounds, &list) != nil || database.GetDB() == nil {
		return nil
	}
	nodeOnly := nodeOnlyOutbounds(list)
	if len(nodeOnly) == 0 {
		return nil
	}
	uses := panelOutboundUses(&settings.SettingService)
	for _, outbound := range list {
		tag, _ := outbound["tag"].(string)
		if _, only := nodeOnly[tag]; only && len(uses[tag]) > 0 {
			return common.NewErrorf("xray template config invalid: outbound %q is used by %s, which the panel's own core serves, so it cannot be limited to agent nodes",
				tag, strings.Join(uses[tag], ", "))
		}
	}
	return nil
}
