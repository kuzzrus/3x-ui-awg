package service

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	// nodeMembersKey is the panel-only key of a balancer that names the nodes it sends traffic out through.
	nodeMembersKey = "nodeMembers"
	// nodeOutboundPrefix starts the tag of each outbound the panel writes for an inbound of a node.
	nodeOutboundPrefix = "xui-node-"
	// nodeSelector is what the page stores as the selector of a balancer of nodes alone: it selects nothing in a panel
	// rolled back to a version that lacks nodeMembers, whose core refuses an empty selector. Here it is dropped.
	nodeSelector = nodeOutboundPrefix

	maxBalancerNodes  = 64
	maxBalancerClient = 256
)

// NodeOutbound is the outbound that one inbound of a node gives the client that logs in to it, as that client dials it.
type NodeOutbound struct {
	NodeID    int
	ClientID  int
	InboundID int
	Outbound  map[string]any
}

var (
	nodeOutboundSource atomic.Pointer[func(email string, nodeIDs []int) ([]NodeOutbound, error)]
	givenNodeOutbounds atomic.Value // the fingerprint of the node outbounds the panel's core was last given
)

// SetNodeOutboundSource sets where the outbounds of the inbounds that a client has on the nodes come from.
// internal/sub writes them for the JSON subscription and imports this package, so the web layer hands it over.
func SetNodeOutboundSource(source func(email string, nodeIDs []int) ([]NodeOutbound, error)) {
	nodeOutboundSource.Store(&source)
}

// nodeMembers is who a balancer sends traffic out through: the nodes, reached as one client of the panel.
type nodeMembers struct {
	client  string
	nodeIDs []int
}

// parseNodeMembers reads the nodeMembers of a balancer: the email of the client and the ids of the nodes.
func parseNodeMembers(raw any) (nodeMembers, error) {
	obj, ok := raw.(map[string]any)
	if !ok {
		return nodeMembers{}, errors.New("must be an object with a client and nodeIds")
	}
	var members nodeMembers
	if rawClient, has := obj["client"]; has && rawClient != nil {
		client, isString := rawClient.(string)
		if !isString {
			return nodeMembers{}, errors.New("client must be the email of a client")
		}
		members.client = strings.TrimSpace(client)
		if utf8.RuneCountInString(members.client) > maxBalancerClient {
			return nodeMembers{}, fmt.Errorf("client is longer than %d characters", maxBalancerClient)
		}
	}
	if rawIDs, has := obj["nodeIds"]; has && rawIDs != nil {
		list, isList := rawIDs.([]any)
		if !isList {
			return nodeMembers{}, errors.New("nodeIds must be a list of node ids")
		}
		for _, item := range list {
			id, isNumber := item.(float64)
			if !isNumber || id != math.Trunc(id) || id < 1 || id > math.MaxInt32 {
				return nodeMembers{}, errors.New("nodeIds must be a list of node ids")
			}
			members.nodeIDs = append(members.nodeIDs, int(id))
		}
		slices.Sort(members.nodeIDs)
		members.nodeIDs = slices.Compact(members.nodeIDs)
		if len(members.nodeIDs) > maxBalancerNodes {
			return nodeMembers{}, fmt.Errorf("a balancer takes at most %d nodes", maxBalancerNodes)
		}
	}
	if len(members.nodeIDs) > 0 && members.client == "" {
		return nodeMembers{}, errors.New("needs the client that logs in to the nodes")
	}
	return members, nil
}

// checkBalancerNodeMembers refuses a balancer whose nodeMembers are not a client and a list of node ids.
func checkBalancerNodeMembers(routerCfg json_util.RawMessage) error {
	var routing struct {
		Balancers []map[string]any `json:"balancers"`
	}
	if len(routerCfg) == 0 || json.Unmarshal(routerCfg, &routing) != nil {
		return nil
	}
	for i, balancer := range routing.Balancers {
		raw, has := balancer[nodeMembersKey]
		if !has {
			continue
		}
		if _, err := parseNodeMembers(raw); err != nil {
			return common.NewErrorf("xray template config invalid: balancer %d: nodeMembers %v", i+1, err)
		}
	}
	return nil
}

// storedSelector is the selector of a balancer of the template without the placeholder that stands for its nodes.
func storedSelector(balancer map[string]any) []any {
	selector, _ := balancer["selector"].([]any)
	kept := make([]any, 0, len(selector))
	for _, entry := range selector {
		if entry != nodeSelector {
			kept = append(kept, entry)
		}
	}
	return kept
}

// leaveNodesOutOfBalancers takes the nodes out of the balancers of an agent's config, which has no outbound to a node.
// A balancer keeps the rest of its selector; one left selecting nothing is held as on the panel, and listed if a rule leads to it.
func leaveNodesOutOfBalancers(cfg *xray.Config) []string {
	var routing map[string]any
	if len(cfg.RouterConfig) == 0 || json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return nil
	}
	have := outboundTagSet(cfg.OutboundConfigs)
	unreachable := map[string]string{}
	changed := false
	balancers, _ := routing["balancers"].([]any)
	for _, raw := range balancers {
		balancer, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if _, has := balancer[nodeMembersKey]; !has {
			continue
		}
		changed = true
		delete(balancer, nodeMembersKey)
		selector := storedSelector(balancer)
		balancer["selector"] = selector
		if !selectsAny(have, selector) {
			tag, _ := balancer["tag"].(string)
			unreachable[tag], _ = balancer["fallbackTag"].(string)
		}
	}
	if !changed {
		return nil
	}
	led := map[string]bool{}
	rules, _ := routing["rules"].([]any)
	for _, raw := range rules {
		if rule, ok := raw.(map[string]any); ok {
			if target, _ := rule["balancerTag"].(string); target != "" {
				led[target] = true
			}
		}
	}
	var notes []string
	for _, held := range holdUnreachable(cfg, routing, unreachable, have) {
		if led[held.balancer] {
			notes = append(notes, fmt.Sprintf("a routing rule sends to balancer %q, which sends traffic out through nodes, and an agent has no outbound to a node: its traffic %s",
				held.balancer, held.outcome()))
		}
	}
	if raw, err := json.Marshal(routing); err == nil {
		cfg.RouterConfig = raw
	}
	return notes
}

// addBlackhole adds the blackhole outbound that takes the traffic of a rule that leads nowhere, unless the config has it.
func addBlackhole(cfg *xray.Config) {
	if outboundTagSet(cfg.OutboundConfigs)[missingOutboundTag] {
		return
	}
	var list []json.RawMessage
	if len(cfg.OutboundConfigs) > 0 && json.Unmarshal(cfg.OutboundConfigs, &list) != nil {
		return
	}
	list = append(list, json.RawMessage(`{"protocol":"blackhole","tag":"`+missingOutboundTag+`"}`))
	if raw, err := json.Marshal(list); err == nil {
		cfg.OutboundConfigs = raw
	}
}

// nodeBalancer is a balancer of the template that sends traffic out through nodes.
type nodeBalancer struct {
	balancer map[string]any
	members  nodeMembers
	probed   bool
	observes bool // picks by what the observatory saw, and so nothing until it has seen something
}

// nodeBalancersOf lists the balancers of a routing section that carry nodeMembers. One that cannot be read is
// listed with no members, which leaves it empty.
func nodeBalancersOf(routing map[string]any) []nodeBalancer {
	balancers, _ := routing["balancers"].([]any)
	var found []nodeBalancer
	for _, raw := range balancers {
		balancer, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rawMembers, has := balancer[nodeMembersKey]
		if !has {
			continue
		}
		members, err := parseNodeMembers(rawMembers)
		if err != nil {
			tag, _ := balancer["tag"].(string)
			logger.Warning("balancer", tag, "has unreadable nodeMembers, taken as empty:", err)
		}
		strategy, _ := balancer["strategy"].(map[string]any)
		kind, _ := strategy["type"].(string)
		fallback, _ := balancer["fallbackTag"].(string)
		observes := kind == "leastPing" || kind == "leastLoad"
		found = append(found, nodeBalancer{
			balancer: balancer,
			members:  members,
			probed:   observes || fallback != "",
			observes: observes,
		})
	}
	return found
}

// writtenNodeOutbound is one outbound the panel writes for an inbound of a node.
type writtenNodeOutbound struct {
	node, client, inbound int
	tag                   string
	raw                   json.RawMessage
}

// nodeOutboundPlan is what the nodes of the balancers come to: the outbounds to write, and for each balancer the
// selector prefixes that reach them.
type nodeOutboundPlan struct {
	outbounds []writtenNodeOutbound
	prefixes  [][]string
}

// fingerprint tells two plans apart: it changes when a node, an inbound or the client changes what is written.
func (p nodeOutboundPlan) fingerprint() string {
	if len(p.outbounds) == 0 && !slices.ContainsFunc(p.prefixes, func(prefixes []string) bool { return len(prefixes) > 0 }) {
		return ""
	}
	sum := sha256.New()
	for _, written := range p.outbounds {
		sum.Write(written.raw)
	}
	for _, prefixes := range p.prefixes {
		sum.Write([]byte(strings.Join(prefixes, ",") + ";"))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// planNodeOutbounds asks the source for the outbounds of each client of the balancers on the nodes that are on.
// An outbound the core would refuse is left out, so one bad inbound cannot keep the config from loading.
func planNodeOutbounds(balancers []nodeBalancer, quiet bool) nodeOutboundPlan {
	warn := func(args ...any) {
		if !quiet {
			logger.Warning(args...)
		}
	}
	plan := nodeOutboundPlan{prefixes: make([][]string, len(balancers))}
	wanted := map[string][]int{}
	var allNodes []int
	for _, b := range balancers {
		if b.members.client == "" || len(b.members.nodeIDs) == 0 {
			continue
		}
		key := strings.ToLower(b.members.client)
		wanted[key] = append(wanted[key], b.members.nodeIDs...)
		allNodes = append(allNodes, b.members.nodeIDs...)
	}
	if len(wanted) == 0 {
		return plan
	}
	sourcePtr := nodeOutboundSource.Load()
	if sourcePtr == nil {
		warn("balancers made of nodes have no source of outbounds, so they stay empty")
		return plan
	}
	var enabled []int
	if err := database.GetDB().Model(&model.Node{}).Where("id IN ? AND enable = ?", allNodes, true).Pluck("id", &enabled).Error; err != nil {
		warn("node balancers: reading the nodes failed:", err)
		return plan
	}

	reached := map[string]map[int]int{} // client -> node -> id of the client, for the nodes that have an outbound
	written := map[string]writtenNodeOutbound{}
	for _, b := range balancers {
		key := strings.ToLower(b.members.client)
		if _, done := reached[key]; done || b.members.client == "" || len(b.members.nodeIDs) == 0 {
			continue
		}
		reached[key] = map[int]int{}
		ids := slices.DeleteFunc(slices.Clone(wanted[key]), func(id int) bool { return !slices.Contains(enabled, id) })
		slices.Sort(ids)
		ids = slices.Compact(ids)
		if len(ids) == 0 {
			continue
		}
		outbounds, err := (*sourcePtr)(b.members.client, ids)
		if err != nil {
			warn("node balancers: the outbounds of client", b.members.client, "failed:", err)
			continue
		}
		for _, item := range outbounds {
			if item.Outbound == nil || !slices.Contains(ids, item.NodeID) {
				continue
			}
			tag := fmt.Sprintf("%s%d-%d-%d", nodeOutboundPrefix, item.NodeID, item.ClientID, item.InboundID)
			item.Outbound["tag"] = tag
			raw, err := json.Marshal(item.Outbound)
			if err == nil {
				err = xray.ValidateOutboundConfig(raw)
			}
			if err != nil {
				warn("node balancers: outbound", tag, "left out, the core refuses it:", err)
				continue
			}
			written[tag] = writtenNodeOutbound{node: item.NodeID, client: item.ClientID, inbound: item.InboundID, tag: tag, raw: raw}
			reached[key][item.NodeID] = item.ClientID
		}
	}

	for i, b := range balancers {
		for _, id := range b.members.nodeIDs {
			if clientID, ok := reached[strings.ToLower(b.members.client)][id]; ok {
				plan.prefixes[i] = append(plan.prefixes[i], fmt.Sprintf("%s%d-%d-", nodeOutboundPrefix, id, clientID))
			}
		}
	}
	for _, item := range written {
		plan.outbounds = append(plan.outbounds, item)
	}
	slices.SortFunc(plan.outbounds, func(a, b writtenNodeOutbound) int {
		return cmp.Or(cmp.Compare(a.node, b.node), cmp.Compare(a.client, b.client), cmp.Compare(a.inbound, b.inbound))
	})
	return plan
}

// expandNodeBalancers writes the outbounds to the nodes of the balancers that carry nodeMembers into the panel's
// config, and returns their fingerprint. The core refuses a balancer with no selector, so see holdUnreachable.
func (s *XrayService) expandNodeBalancers(cfg *xray.Config) string {
	var routing map[string]any
	if len(cfg.RouterConfig) == 0 || json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return ""
	}
	balancers := nodeBalancersOf(routing)
	if len(balancers) == 0 {
		return ""
	}
	plan := planNodeOutbounds(balancers, false)
	fingerprint := plan.fingerprint()

	var outbounds []json.RawMessage
	if len(cfg.OutboundConfigs) > 0 {
		_ = json.Unmarshal(cfg.OutboundConfigs, &outbounds)
	}
	have := outboundTagSet(cfg.OutboundConfigs)
	appended := map[string]bool{}
	if len(outbounds) == 0 && len(plan.outbounds) > 0 {
		logger.Warning("node balancers: the template has no outbound, so none is written: the first one is the default")
	} else {
		for _, written := range plan.outbounds {
			if have[written.tag] {
				logger.Warning("node balancers: outbound", written.tag, "is already in the template, so it is not written")
				continue
			}
			outbounds = append(outbounds, written.raw)
			have[written.tag] = true
			appended[written.tag] = true
		}
	}

	var probed []string
	unreachable := map[string]string{} // balancer tag -> its fallback
	blocksWhenBlind := false
	for i, b := range balancers {
		selector := storedSelector(b.balancer)
		nodesOnly := len(selector) == 0
		for _, prefix := range plan.prefixes[i] {
			if !anyTagHasPrefix(appended, prefix) {
				continue
			}
			if !slices.Contains(selector, any(prefix)) {
				selector = append(selector, prefix)
			}
		}
		if b.probed {
			// Whatever the nodes that are on, or the observatory would change, and a change of it restarts the core.
			for _, id := range b.members.nodeIDs {
				probed = append(probed, fmt.Sprintf("%s%d-", nodeOutboundPrefix, id))
			}
		}
		b.balancer["selector"] = selector
		delete(b.balancer, nodeMembersKey)
		fallback, _ := b.balancer["fallbackTag"].(string)
		switch {
		case !selectsAny(have, selector):
			tag, _ := b.balancer["tag"].(string)
			unreachable[tag] = fallback
		case b.observes && nodesOnly && fallback == "":
			// Until the observatory has seen a node, or when it sees none alive, the core would pick nothing for it.
			b.balancer["fallbackTag"] = missingOutboundTag
			blocksWhenBlind = true
		}
	}

	if len(appended) > 0 {
		raw, err := json.Marshal(outbounds)
		if err != nil {
			return fingerprint
		}
		cfg.OutboundConfigs = raw
	}
	if blocksWhenBlind {
		addBlackhole(cfg)
	}
	if len(probed) > 0 {
		cfg.Observatory = extendSubjectSelector(cfg.Observatory, probed)
		cfg.BurstObservatory = extendSubjectSelector(cfg.BurstObservatory, probed)
	}
	for _, held := range holdUnreachable(cfg, routing, unreachable, have) {
		logger.Warning("node balancers: balancer", held.balancer, "reaches no outbound, so its traffic", held.outcome())
	}
	if raw, err := json.Marshal(routing); err == nil {
		cfg.RouterConfig = raw
	}
	return fingerprint
}

// noOutbound is a selector entry that no outbound has: a balancer that holds it selects nothing.
const noOutbound = "x-ui-no-outbound-"

// redirect is where a balancer that reaches no outbound sends its traffic: to its fallback, or into the blackhole.
type redirect struct{ balancer, to string }

func (r redirect) outcome() string {
	if r.to == missingOutboundTag {
		return "is blocked"
	}
	return fmt.Sprintf("goes to its fallback %q", r.to)
}

// holdUnreachable keeps the balancers that reach no outbound, for the core refuses an empty selector, and sends them to
// the fallback when the config has that outbound, and into the blackhole otherwise: not out of the default outbound.
func holdUnreachable(cfg *xray.Config, routing map[string]any, unreachable map[string]string, have map[string]bool) []redirect {
	if len(unreachable) == 0 {
		return nil
	}
	var held []redirect
	blocked := false
	balancers, _ := routing["balancers"].([]any)
	for _, raw := range balancers {
		balancer, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		tag, _ := balancer["tag"].(string)
		fallback, gone := unreachable[tag]
		if !gone {
			continue
		}
		if fallback != "" && have[fallback] {
			balancer["selector"] = []any{noOutbound}
			held = append(held, redirect{balancer: tag, to: fallback})
			continue
		}
		// A strategy that probes would look for the blackhole among the outbounds it observes, and there it is not.
		balancer["selector"] = []any{missingOutboundTag}
		balancer["strategy"] = map[string]any{"type": "random"}
		delete(balancer, "fallbackTag")
		blocked = true
		held = append(held, redirect{balancer: tag, to: missingOutboundTag})
	}
	if blocked {
		addBlackhole(cfg)
	}
	return held
}

// anyTagHasPrefix reports whether one of the tags starts with the prefix.
func anyTagHasPrefix(tags map[string]bool, prefix string) bool {
	for tag := range tags {
		if strings.HasPrefix(tag, prefix) {
			return true
		}
	}
	return false
}

// selectsAny reports whether the selector reaches one of the outbounds.
func selectsAny(tags map[string]bool, selector []any) bool {
	for tag := range tags {
		for _, entry := range selector {
			if prefix, ok := entry.(string); ok && strings.HasPrefix(tag, prefix) {
				return true
			}
		}
	}
	return false
}

// extendSubjectSelector adds prefixes to the subjectSelector of an observatory, which it leaves alone when there is none.
func extendSubjectSelector(raw json_util.RawMessage, prefixes []string) json_util.RawMessage {
	var section map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &section) != nil {
		return raw
	}
	selector, _ := section["subjectSelector"].([]any)
	for _, prefix := range prefixes {
		if !slices.Contains(selector, any(prefix)) {
			selector = append(selector, prefix)
		}
	}
	section["subjectSelector"] = selector
	out, err := json.Marshal(section)
	if err != nil {
		return raw
	}
	return out
}

// RefreshNodeOutbounds asks for a restart check when the outbounds to the nodes of the balancers are no longer the
// ones the panel's core was given: a node changed a port, a key, or what the client may log in to.
func (s *XrayService) RefreshNodeOutbounds() {
	if isManuallyStopped.Load() {
		return
	}
	template, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return
	}
	cfg := &xray.Config{}
	var routing map[string]any
	if json.Unmarshal([]byte(template), cfg) != nil || len(cfg.RouterConfig) == 0 || json.Unmarshal(cfg.RouterConfig, &routing) != nil {
		return
	}
	var fingerprint string
	if balancers := nodeBalancersOf(routing); len(balancers) > 0 {
		fingerprint = planNodeOutbounds(balancers, true).fingerprint()
	}
	if last, _ := givenNodeOutbounds.Load().(string); fingerprint != last {
		s.SetToNeedRestart()
	}
}

// RemoveNodeReferences takes a deleted node out of the nodeMembers of the balancers of the template.
func (s *XraySettingService) RemoveNodeReferences(nodeID int) (bool, error) {
	template, err := s.GetXrayConfigTemplate()
	if err != nil {
		return false, err
	}
	updated, changed, err := mutateXrayTemplateRouting(template, func(cfg map[string]any) bool {
		routing, _ := cfg["routing"].(map[string]any)
		balancers, _ := routing["balancers"].([]any)
		mutated := false
		for _, raw := range balancers {
			balancer, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			members, ok := balancer[nodeMembersKey].(map[string]any)
			if !ok {
				continue
			}
			ids, ok := members["nodeIds"].([]any)
			if !ok {
				continue
			}
			left := slices.DeleteFunc(slices.Clone(ids), func(id any) bool { n, isNumber := id.(float64); return isNumber && int(n) == nodeID })
			if len(left) != len(ids) {
				members["nodeIds"] = left
				mutated = true
			}
		}
		return mutated
	})
	if err != nil || !changed {
		return false, err
	}
	if err := s.SaveXraySetting(updated); err != nil {
		return false, err
	}
	return true, nil
}
