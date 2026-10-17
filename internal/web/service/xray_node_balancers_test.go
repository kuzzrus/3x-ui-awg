package service

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// vlessOutbound is an outbound the core accepts, as the JSON subscription writes one for a client.
func vlessOutbound(address string, port int) map[string]any {
	return map[string]any{
		"tag":      "proxy",
		"protocol": "vless",
		"settings": map[string]any{"address": address, "port": port, "id": "11111111-2222-4333-8444-555555555555", "encryption": "none"},
		"streamSettings": map[string]any{
			"network": "tcp", "security": "tls", "tlsSettings": map[string]any{"serverName": address},
		},
	}
}

// useNodeOutbounds makes the source serve these outbounds for the clients, by lower-case email.
func useNodeOutbounds(t *testing.T, byClient map[string][]NodeOutbound) {
	t.Helper()
	SetNodeOutboundSource(func(email string, nodeIDs []int) ([]NodeOutbound, error) {
		var out []NodeOutbound
		for _, item := range byClient[strings.ToLower(email)] {
			if slices.Contains(nodeIDs, item.NodeID) {
				out = append(out, item)
			}
		}
		return out, nil
	})
	t.Cleanup(func() { nodeOutboundSource.Store(nil) })
	(&XrayService{}).IsNeedRestartAndSetFalse()
	t.Cleanup(func() { (&XrayService{}).IsNeedRestartAndSetFalse() })
}

func nodeOut(node, client, inbound int, address string, port int) NodeOutbound {
	return NodeOutbound{NodeID: node, ClientID: client, InboundID: inbound, Outbound: vlessOutbound(address, port)}
}

// editTemplate changes the stored template through a function.
func editTemplate(t *testing.T, edit func(template map[string]any)) {
	t.Helper()
	settings := &SettingService{}
	raw, err := settings.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var template map[string]any
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		t.Fatal(err)
	}
	edit(template)
	out, err := json.Marshal(template)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(out)); err != nil {
		t.Fatal(err)
	}
}

// useBalancers adds balancers and the rules that lead to them to the routing of the template.
func useBalancers(t *testing.T, balancers []any, rules ...any) {
	t.Helper()
	editTemplate(t, func(template map[string]any) {
		routing, _ := template["routing"].(map[string]any)
		if routing == nil {
			routing = map[string]any{}
			template["routing"] = routing
		}
		existing, _ := routing["balancers"].([]any)
		routing["balancers"] = append(existing, balancers...)
		existingRules, _ := routing["rules"].([]any)
		routing["rules"] = append(existingRules, rules...)
	})
}

func balancerOfNodes(tag, kind, client string, nodeIDs ...int) map[string]any {
	ids := make([]any, len(nodeIDs))
	for i, id := range nodeIDs {
		ids[i] = id
	}
	return map[string]any{
		"tag": tag, "selector": []any{}, "strategy": map[string]any{"type": kind},
		nodeMembersKey: map[string]any{"client": client, "nodeIds": ids},
	}
}

func ruleToBalancer(domain, balancer string) map[string]any {
	return map[string]any{"type": "field", "domain": []any{domain}, "balancerTag": balancer}
}

func renderPanelConfig(t *testing.T) *xray.Config {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	return cfg
}

func outboundTagList(t *testing.T, cfg *xray.Config) []string {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &list); err != nil {
		t.Fatal(err)
	}
	tags := make([]string, 0, len(list))
	for _, outbound := range list {
		tag, _ := outbound["tag"].(string)
		tags = append(tags, tag)
	}
	return tags
}

func routingOf(t *testing.T, cfg *xray.Config) map[string]any {
	t.Helper()
	var routing map[string]any
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	return routing
}

func balancerNamed(t *testing.T, cfg *xray.Config, tag string) map[string]any {
	t.Helper()
	balancers, _ := routingOf(t, cfg)["balancers"].([]any)
	for _, raw := range balancers {
		if balancer, ok := raw.(map[string]any); ok && balancer["tag"] == tag {
			return balancer
		}
	}
	return nil
}

func selectorOf(balancer map[string]any) []string {
	var out []string
	list, _ := balancer["selector"].([]any)
	for _, entry := range list {
		out = append(out, entry.(string))
	}
	return out
}

func ruleForDomain(t *testing.T, cfg *xray.Config, domain string) map[string]any {
	t.Helper()
	rules, _ := routingOf(t, cfg)["rules"].([]any)
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		if domains, _ := rule["domain"].([]any); len(domains) == 1 && domains[0] == domain {
			return rule
		}
	}
	t.Fatalf("no rule for %s in %s", domain, cfg.RouterConfig)
	return nil
}

func TestNodeBalancerGetsTheOutboundsOfItsNodes(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	paris := seedTaggedAgent(t, "paris")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {
		nodeOut(paris, 7, 21, "paris.example.com", 443),
		nodeOut(berlin, 7, 12, "berlin.example.com", 8443),
		nodeOut(berlin, 7, 11, "berlin.example.com", 443),
	}})
	before := outboundTagList(t, renderPanelConfig(t))
	b := balancerOfNodes("exits", "random", "relay@x", paris, berlin)
	b["selector"] = []any{"direct"}
	useBalancers(t, []any{b}, ruleToBalancer("geosite:test", "exits"))

	cfg := renderPanelConfig(t)
	tags := outboundTagList(t, cfg)

	want := []string{
		fmt.Sprintf("xui-node-%d-7-11", berlin), fmt.Sprintf("xui-node-%d-7-12", berlin), fmt.Sprintf("xui-node-%d-7-21", paris),
	}
	if !slices.Equal(tags[:len(before)], before) || !slices.Equal(tags[len(before):], want) {
		t.Fatalf("outbounds = %v, want the template's %v and then %v", tags, before, want)
	}
	selector := selectorOf(balancerNamed(t, cfg, "exits"))
	if wantSelector := []string{"direct", fmt.Sprintf("xui-node-%d-7-", berlin), fmt.Sprintf("xui-node-%d-7-", paris)}; !slices.Equal(selector, wantSelector) {
		t.Fatalf("selector = %v, want %v", selector, wantSelector)
	}
	if strings.Contains(string(cfg.RouterConfig), nodeMembersKey) {
		t.Fatalf("the panel-only key reached the core: %s", cfg.RouterConfig)
	}
	if rule := ruleForDomain(t, cfg, "geosite:test"); rule["balancerTag"] != "exits" {
		t.Fatalf("the rule no longer leads to the balancer: %v", rule)
	}
	var list []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &list); err != nil {
		t.Fatal(err)
	}
	last := list[len(list)-1]
	if settings, _ := last["settings"].(map[string]any); settings["port"] != float64(443) || settings["address"] != "paris.example.com" {
		t.Fatalf("the last outbound is not the one of paris: %v", last)
	}
}

func TestNodeBalancerRenderIsStable(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {
		nodeOut(berlin, 7, 12, "berlin.example.com", 8443), nodeOut(berlin, 7, 11, "berlin.example.com", 443),
	}})
	useBalancers(t, []any{balancerOfNodes("exits", "leastPing", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))

	if first, second := renderPanelConfig(t), renderPanelConfig(t); !first.Equals(second) {
		t.Fatal("two renders of the same state differ, so the core would restart for nothing")
	}
}

func TestNodeBalancerSkipsNodesThatAreOff(t *testing.T) {
	setupSettingTestDB(t)
	on := seedTaggedAgent(t, "on")
	off := seedTaggedAgent(t, "off")
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", off).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {
		nodeOut(on, 7, 11, "on.example.com", 443), nodeOut(off, 7, 21, "off.example.com", 443),
	}})
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", on, off)}, ruleToBalancer("geosite:test", "exits"))

	cfg := renderPanelConfig(t)
	tags := outboundTagList(t, cfg)
	if slices.Contains(tags, fmt.Sprintf("xui-node-%d-7-21", off)) || !slices.Contains(tags, fmt.Sprintf("xui-node-%d-7-11", on)) {
		t.Fatalf("outbounds = %v, want only the node that is on", tags)
	}
	if selector := selectorOf(balancerNamed(t, cfg, "exits")); !slices.Equal(selector, []string{fmt.Sprintf("xui-node-%d-7-", on)}) {
		t.Fatalf("selector = %v, want the prefix of the node that is on only", selector)
	}
}

func TestNodeBalancerLeavesOutAnOutboundTheCoreRefuses(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	bad := NodeOutbound{NodeID: berlin, ClientID: 7, InboundID: 12, Outbound: map[string]any{"protocol": "no-such-protocol", "settings": map[string]any{}}}
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443), bad}})
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))

	tags := outboundTagList(t, renderPanelConfig(t))
	if !slices.Contains(tags, fmt.Sprintf("xui-node-%d-7-11", berlin)) || slices.Contains(tags, fmt.Sprintf("xui-node-%d-7-12", berlin)) {
		t.Fatalf("outbounds = %v, want the good outbound of the node and not the one the core refuses", tags)
	}
}

func TestNodeBalancerAsksTheObservatoryToProbeTheNodes(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	paris := seedTaggedAgent(t, "paris")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {
		nodeOut(berlin, 7, 11, "berlin.example.com", 443), nodeOut(paris, 7, 21, "paris.example.com", 443),
	}})
	useBalancers(t, []any{
		balancerOfNodes("by-ping", "leastPing", "relay@x", berlin),
		balancerOfNodes("by-chance", "random", "relay@x", paris),
	}, ruleToBalancer("geosite:a", "by-ping"), ruleToBalancer("geosite:b", "by-chance"))
	editTemplate(t, func(template map[string]any) {
		template["observatory"] = map[string]any{"subjectSelector": []any{"direct"}, "probeURL": "https://probe.example/204"}
	})

	cfg := renderPanelConfig(t)
	var observatory struct {
		SubjectSelector []string `json:"subjectSelector"`
		ProbeURL        string   `json:"probeURL"`
	}
	if err := json.Unmarshal(cfg.Observatory, &observatory); err != nil {
		t.Fatal(err)
	}
	if want := []string{"direct", fmt.Sprintf("xui-node-%d-7-", berlin)}; !slices.Equal(observatory.SubjectSelector, want) || observatory.ProbeURL != "https://probe.example/204" {
		t.Fatalf("observatory = %+v, want the nodes of the balancer that probes added to its selector, and nothing else changed", observatory)
	}
	if len(cfg.BurstObservatory) != 0 {
		t.Fatalf("a burst observatory appeared: %s", cfg.BurstObservatory)
	}
}

func TestNodeBalancerWithNoOutboundBlocksItsRules(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	kept := balancerOfNodes("kept", "random", "relay@x", berlin)
	kept["fallbackTag"] = "direct"
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin), kept},
		ruleToBalancer("geosite:empty", "exits"), ruleToBalancer("geosite:fallback", "kept"))

	cfg := renderPanelConfig(t)
	if rule := ruleForDomain(t, cfg, "geosite:empty"); rule["outboundTag"] != missingOutboundTag || rule["balancerTag"] != nil {
		t.Fatalf("a rule that leads to a balancer with no outbound must block its traffic, got %v", rule)
	}
	if rule := ruleForDomain(t, cfg, "geosite:fallback"); rule["balancerTag"] != "kept" {
		t.Fatalf("a balancer with a fallback is left as it is, got %v", rule)
	}
	tags := outboundTagList(t, cfg)
	if n := slices.Index(tags, missingOutboundTag); n < 0 || slices.Index(tags[n+1:], missingOutboundTag) >= 0 {
		t.Fatalf("want one blackhole %s among %v", missingOutboundTag, tags)
	}
}

func TestNodeBalancerWithoutASourceBlocksItsRules(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	nodeOutboundSource.Store(nil)
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))

	cfg := renderPanelConfig(t)
	if rule := ruleForDomain(t, cfg, "geosite:test"); rule["outboundTag"] != missingOutboundTag {
		t.Fatalf("got %v, want the rule to block", rule)
	}
	if strings.Contains(string(cfg.RouterConfig), nodeMembersKey) {
		t.Fatalf("the panel-only key reached the core: %s", cfg.RouterConfig)
	}
}

func TestNodeBalancerKeepsTheTemplateOutboundWithTheSameTag(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443)}})
	addTemplateOutbounds(t, hop(fmt.Sprintf("xui-node-%d-7-11", berlin), nil))
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))

	tags := outboundTagList(t, renderPanelConfig(t))
	count := 0
	for _, tag := range tags {
		if tag == fmt.Sprintf("xui-node-%d-7-11", berlin) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the tag is in the config %d times, want the template's one only", count)
	}
}

func TestNodeBalancersShareTheOutboundsOfOneClient(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	paris := seedTaggedAgent(t, "paris")
	useNodeOutbounds(t, map[string][]NodeOutbound{
		"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443), nodeOut(paris, 7, 21, "paris.example.com", 443)},
		"other@x": {nodeOut(berlin, 9, 11, "berlin.example.com", 443)},
	})
	useBalancers(t, []any{
		balancerOfNodes("first", "random", "relay@x", berlin, paris),
		balancerOfNodes("second", "random", "Relay@X", berlin),
		balancerOfNodes("third", "random", "other@x", berlin),
	}, ruleToBalancer("geosite:a", "first"), ruleToBalancer("geosite:b", "second"), ruleToBalancer("geosite:c", "third"))

	cfg := renderPanelConfig(t)
	tags := outboundTagList(t, cfg)
	for _, tag := range []string{fmt.Sprintf("xui-node-%d-7-11", berlin), fmt.Sprintf("xui-node-%d-7-21", paris), fmt.Sprintf("xui-node-%d-9-11", berlin)} {
		if n := strings.Count(strings.Join(tags, " ")+" ", tag+" "); n != 1 {
			t.Fatalf("outbound %s is in the config %d times, want once; tags = %v", tag, n, tags)
		}
	}
	if got := selectorOf(balancerNamed(t, cfg, "third")); !slices.Equal(got, []string{fmt.Sprintf("xui-node-%d-9-", berlin)}) {
		t.Fatalf("the balancer of the other client selects %v, want its own prefix only", got)
	}
	if got := selectorOf(balancerNamed(t, cfg, "second")); !slices.Equal(got, []string{fmt.Sprintf("xui-node-%d-7-", berlin)}) {
		t.Fatalf("the client matches without regard to case, got %v", got)
	}
}

func TestAConfigWithoutNodeBalancersIsLeftAsItWas(t *testing.T) {
	setupSettingTestDB(t)
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	svc := &XrayService{}
	cfg, _, err := svc.newConfigFromTemplate(configScope{})
	if err != nil {
		t.Fatal(err)
	}
	routing, outbounds := string(cfg.RouterConfig), string(cfg.OutboundConfigs)
	svc.expandNodeBalancers(cfg)
	if string(cfg.RouterConfig) != routing || string(cfg.OutboundConfigs) != outbounds {
		t.Fatal("a template with no balancer of nodes was rewritten")
	}
}

func TestAgentConfigLeavesOutTheBalancersMadeOfNodes(t *testing.T) {
	setupSettingTestDB(t)
	agent := seedTaggedAgent(t, "agent-1")
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443)}})
	plain := map[string]any{"tag": "plain", "selector": []any{"direct"}}
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin), plain},
		ruleToBalancer("geosite:nodes", "exits"), ruleToBalancer("geosite:plain", "plain"))

	svc := &XrayService{}
	agentCfg, err := svc.GetAgentConfig(agent)
	if err != nil {
		t.Fatal(err)
	}
	if balancerNamed(t, agentCfg, "exits") != nil || balancerNamed(t, agentCfg, "plain") == nil {
		t.Fatalf("an agent must lose the balancer of nodes and keep the other: %s", agentCfg.RouterConfig)
	}
	rules, _ := routingOf(t, agentCfg)["rules"].([]any)
	for _, raw := range rules {
		if rule, _ := raw.(map[string]any); rule["balancerTag"] == "exits" {
			t.Fatalf("a rule that leads to the balancer of nodes stayed in the config of an agent: %v", rule)
		}
	}
	if rule := ruleForDomain(t, agentCfg, "geosite:plain"); rule["balancerTag"] != "plain" {
		t.Fatalf("the rule of the other balancer must stay, got %v", rule)
	}
	if strings.Contains(string(agentCfg.RouterConfig), nodeMembersKey) || slices.ContainsFunc(outboundTagList(t, agentCfg), func(tag string) bool { return strings.HasPrefix(tag, nodeOutboundPrefix) }) {
		t.Fatalf("an agent got something of the nodes: %s", agentCfg.RouterConfig)
	}

	panelCfg := renderPanelConfig(t)
	if balancerNamed(t, panelCfg, "exits") == nil || ruleForDomain(t, panelCfg, "geosite:nodes")["balancerTag"] != "exits" {
		t.Fatalf("the panel's own core must keep the balancer of nodes and its rule: %s", panelCfg.RouterConfig)
	}
}

func TestTemplateCheckRefusesNodeMembersThatAreNotAClientAndNodes(t *testing.T) {
	setupSettingTestDB(t)
	svc := &XraySettingService{}
	template := func(members string) string {
		return `{"routing":{"balancers":[{"tag":"b","selector":[],"nodeMembers":` + members + `}]}}`
	}
	for _, ok := range []string{
		`{"client":"relay@x","nodeIds":[1,2]}`,
		`{"client":"relay@x","nodeIds":[]}`,
		`{"client":"","nodeIds":[]}`,
		`{}`,
	} {
		if err := svc.CheckXrayConfig(template(ok)); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
	long := strings.Repeat("x", maxBalancerClient+1)
	var many []string
	for i := 1; i <= maxBalancerNodes+1; i++ {
		many = append(many, fmt.Sprint(i))
	}
	for _, bad := range []string{
		`"relay@x"`, `[1]`, `{"client":5,"nodeIds":[1]}`, `{"client":"relay@x","nodeIds":"1"}`,
		`{"client":"relay@x","nodeIds":["a"]}`, `{"client":"relay@x","nodeIds":[0]}`, `{"client":"relay@x","nodeIds":[1.5]}`,
		`{"client":"relay@x","nodeIds":[-3]}`, `{"nodeIds":[1]}`, `{"client":"  ","nodeIds":[1]}`,
		`{"client":"` + long + `"}`, `{"client":"relay@x","nodeIds":[` + strings.Join(many, ",") + `]}`,
	} {
		if err := svc.CheckXrayConfig(template(bad)); err == nil {
			t.Errorf("%.60s was accepted", bad)
		}
	}
}

func TestDeletingANodeTakesItOutOfTheBalancersOfTheTemplate(t *testing.T) {
	setupSettingTestDB(t)
	gone := seedTaggedAgent(t, "gone")
	kept := seedTaggedAgent(t, "kept")
	// A template of its own: saving the default one needs the geo files of the core.
	seed, err := json.Marshal(map[string]any{
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
		"routing": map[string]any{"balancers": []any{
			balancerOfNodes("both", "random", "relay@x", gone, kept),
			balancerOfNodes("only", "random", "relay@x", gone),
			map[string]any{"tag": "plain", "selector": []any{"direct"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	seedXrayTemplate(t, string(seed))
	(&XrayService{}).IsNeedRestartAndSetFalse()

	if err := (&NodeService{}).Delete(gone); err != nil {
		t.Fatal(err)
	}

	raw, err := (&SettingService{}).GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var template struct {
		Routing struct {
			Balancers []map[string]any `json:"balancers"`
		} `json:"routing"`
	}
	if err := json.Unmarshal([]byte(raw), &template); err != nil {
		t.Fatal(err)
	}
	ids := map[string][]any{}
	for _, balancer := range template.Routing.Balancers {
		if members, ok := balancer[nodeMembersKey].(map[string]any); ok {
			ids[balancer["tag"].(string)], _ = members["nodeIds"].([]any)
		}
	}
	if len(ids["both"]) != 1 || ids["both"][0] != float64(kept) || len(ids["only"]) != 0 {
		t.Fatalf("nodeIds after the delete = %v, want [%d] and none", ids, kept)
	}
	if !(&XrayService{}).IsNeedRestartAndSetFalse() {
		t.Fatal("the core was not asked to check its config after the template changed")
	}
}

func TestRefreshNodeOutboundsAsksForACheckWhenTheNodesChange(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	port := 443
	SetNodeOutboundSource(func(string, []int) ([]NodeOutbound, error) {
		return []NodeOutbound{nodeOut(berlin, 7, 11, "berlin.example.com", port)}, nil
	})
	t.Cleanup(func() { nodeOutboundSource.Store(nil) })
	svc := &XrayService{}
	svc.IsNeedRestartAndSetFalse()
	t.Cleanup(func() { svc.IsNeedRestartAndSetFalse() })

	renderedNodeOutbound.Store("")
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("a template with no balancer of nodes asked for a check")
	}

	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))
	renderPanelConfig(t)
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("nothing changed since the config was written, yet a check was asked for")
	}

	port = 8443
	svc.RefreshNodeOutbounds()
	if !svc.IsNeedRestartAndSetFalse() {
		t.Fatal("the node changed a port, but no check was asked for")
	}

	renderPanelConfig(t)
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("a check was asked for again after the config was written")
	}
}
