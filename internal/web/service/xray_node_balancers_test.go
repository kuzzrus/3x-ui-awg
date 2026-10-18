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

	"github.com/xtls/xray-core/infra/conf"
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
		"tag": tag, "selector": []any{nodeSelector}, "strategy": map[string]any{"type": kind},
		nodeMembersKey: map[string]any{"client": client, "nodeIds": ids},
	}
}

func ruleToBalancer(domain, balancer string) map[string]any {
	return map[string]any{"type": "field", "domain": []any{domain}, "balancerTag": balancer}
}

// coreTakesTheRules checks what the core refuses a config for besides its balancers: two outbounds with one tag, and
// a rule that names neither an outbound nor a balancer.
func coreTakesTheRules(t *testing.T, cfg *xray.Config) {
	t.Helper()
	outboundTagsOf(t, cfg)
	rules, _ := routingOf(t, cfg)["rules"].([]any)
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		outbound, _ := rule["outboundTag"].(string)
		balancer, _ := rule["balancerTag"].(string)
		if outbound == "" && balancer == "" {
			t.Errorf("the core refuses a rule that names neither an outbound nor a balancer: %v", rule)
		}
	}
}

// renderPanelConfig builds the config of the panel's own core, as the page that shows it does: it is given to nothing.
func renderPanelConfig(t *testing.T) *xray.Config {
	t.Helper()
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	coreTakesTheRules(t, cfg)
	return cfg
}

// giveTheCore does what a restart does once the core has the config it built: it notes the node outbounds.
func giveTheCore(t *testing.T) *xray.Config {
	t.Helper()
	cfg, nodeOutbounds, err := (&XrayService{}).buildXrayConfig()
	if err != nil {
		t.Fatalf("buildXrayConfig: %v", err)
	}
	givenNodeOutbounds.Store(nodeOutbounds)
	return cfg
}

// coreBuildsBalancers builds the balancers of the config as xray-core does, which refuses one with no selector
// and so the whole config.
func coreBuildsBalancers(t *testing.T, cfg *xray.Config) {
	t.Helper()
	var routing struct {
		Balancers json.RawMessage `json:"balancers"`
	}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	if len(routing.Balancers) == 0 {
		routing.Balancers = json.RawMessage("[]")
	}
	router := new(conf.RouterConfig)
	if err := json.Unmarshal([]byte(`{"balancers":`+string(routing.Balancers)+`}`), router); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Build(); err != nil {
		t.Fatalf("the core refuses the balancers of %s: %v", cfg.RouterConfig, err)
	}
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

// The core refuses a balancer with an empty selector, and the whole config with it, so one that reaches no outbound is
// left out; the rules that led to it go to its fallback, or block.
func TestNodeBalancerThatReachesNothingIsLeftOutAndItsRulesAreKeptSafe(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	withFallback := balancerOfNodes("with-fallback", "random", "relay@x", berlin)
	withFallback["fallbackTag"] = "direct"
	lostFallback := balancerOfNodes("lost-fallback", "random", "relay@x", berlin)
	lostFallback["fallbackTag"] = "no-such-outbound"
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin), withFallback, lostFallback},
		ruleToBalancer("geosite:empty", "exits"), ruleToBalancer("geosite:fallback", "with-fallback"),
		ruleToBalancer("geosite:lost", "lost-fallback"))

	cfg := renderPanelConfig(t)
	coreBuildsBalancers(t, cfg)
	for _, tag := range []string{"exits", "with-fallback", "lost-fallback"} {
		if balancerNamed(t, cfg, tag) != nil {
			t.Fatalf("balancer %s reaches nothing and must not be in the config: %s", tag, cfg.RouterConfig)
		}
	}
	if rule := ruleForDomain(t, cfg, "geosite:empty"); rule["outboundTag"] != missingOutboundTag || rule["balancerTag"] != nil {
		t.Fatalf("a rule that led to a balancer with no outbound must block its traffic, got %v", rule)
	}
	if rule := ruleForDomain(t, cfg, "geosite:fallback"); rule["outboundTag"] != "direct" || rule["balancerTag"] != nil {
		t.Fatalf("a rule that led to a balancer with a fallback must send to the fallback, got %v", rule)
	}
	if rule := ruleForDomain(t, cfg, "geosite:lost"); rule["outboundTag"] != missingOutboundTag {
		t.Fatalf("a fallback the config lacks must not be sent to, got %v", rule)
	}
	tags := outboundTagList(t, cfg)
	if n := slices.Index(tags, missingOutboundTag); n < 0 || slices.Index(tags[n+1:], missingOutboundTag) >= 0 {
		t.Fatalf("want one blackhole %s among %v", missingOutboundTag, tags)
	}
}

func TestNodeBalancerWithoutASourceIsLeftOutAndItsRulesBlock(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	nodeOutboundSource.Store(nil)
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))

	cfg := renderPanelConfig(t)
	coreBuildsBalancers(t, cfg)
	if rule := ruleForDomain(t, cfg, "geosite:test"); rule["outboundTag"] != missingOutboundTag {
		t.Fatalf("got %v, want the rule to block", rule)
	}
	if strings.Contains(string(cfg.RouterConfig), nodeMembersKey) {
		t.Fatalf("the panel-only key reached the core: %s", cfg.RouterConfig)
	}
}

func TestNodeBalancerKeepsItsOwnOutboundsWhenNoNodeIsReached(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	b := balancerOfNodes("mixed", "random", "relay@x", berlin)
	b["selector"] = []any{"direct"}
	useBalancers(t, []any{b}, ruleToBalancer("geosite:test", "mixed"))

	cfg := renderPanelConfig(t)
	coreBuildsBalancers(t, cfg)
	if got := selectorOf(balancerNamed(t, cfg, "mixed")); !slices.Equal(got, []string{"direct"}) {
		t.Fatalf("selector = %v, want the outbound of the template alone", got)
	}
	if rule := ruleForDomain(t, cfg, "geosite:test"); rule["balancerTag"] != "mixed" || rule["outboundTag"] != nil {
		t.Fatalf("a balancer that still reaches an outbound keeps its rules, got %v", rule)
	}
}

// The blackhole that blocks the rules of a balancer that reaches nothing and the one that blocks the rules of an
// outbound the panel's core lacks are one outbound: the core refuses two with the same tag.
func TestTwoReasonsToBlockAddOneBlackhole(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	addTemplateOutbounds(t, hop("eu-only", map[string]any{"nodeTags": []any{"eu"}}))
	addTemplateRules(t, markedRule("geosite:eu", map[string]any{"outboundTag": "eu-only"}))
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:empty", "exits"))

	cfg := renderPanelConfig(t)
	for _, domain := range []string{"geosite:eu", "geosite:empty"} {
		if rule := ruleForDomain(t, cfg, domain); rule["outboundTag"] != missingOutboundTag {
			t.Fatalf("the rule for %s must block, got %v", domain, rule)
		}
	}
	count := 0
	for _, tag := range outboundTagList(t, cfg) {
		if tag == missingOutboundTag {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("the blackhole %s is in the config %d times, want once", missingOutboundTag, count)
	}
}

// An outbound with no tag is legal config, and its empty tag must not pass for the fallback of a balancer that has none.
func TestNodeBalancerWithNoFallbackBlocksWhenAnOutboundHasNoTag(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{})
	addTemplateOutbounds(t, map[string]any{"protocol": "freedom"})
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:empty", "exits"))

	cfg := renderPanelConfig(t)
	if rule := ruleForDomain(t, cfg, "geosite:empty"); rule["outboundTag"] != missingOutboundTag {
		t.Fatalf("a rule that led to a balancer with no fallback must block, got %v", rule)
	}
	if !slices.Contains(outboundTagList(t, cfg), missingOutboundTag) {
		t.Fatalf("the blackhole is missing from %v", outboundTagList(t, cfg))
	}
}

// The page stores a balancer of nodes alone with a placeholder as its selector, which a panel rolled back to a version
// without nodeMembers can load. The core here must never see it: it would select the nodes of every balancer.
func TestNodeBalancerDropsThePlaceholderOfItsStoredSelector(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	paris := seedTaggedAgent(t, "paris")
	useNodeOutbounds(t, map[string][]NodeOutbound{
		"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443)},
		"other@x": {nodeOut(paris, 9, 21, "paris.example.com", 443)},
	})
	legacy := balancerOfNodes("legacy", "random", "other@x", paris)
	legacy["selector"] = []any{}
	mixed := balancerOfNodes("mixed", "random", "relay@x", berlin)
	mixed["selector"] = []any{"direct", nodeSelector}
	useBalancers(t, []any{balancerOfNodes("stored", "random", "relay@x", berlin), legacy, mixed},
		ruleToBalancer("geosite:a", "stored"), ruleToBalancer("geosite:b", "legacy"), ruleToBalancer("geosite:c", "mixed"))

	cfg := renderPanelConfig(t)
	coreBuildsBalancers(t, cfg)
	berlinPrefix, parisPrefix := fmt.Sprintf("xui-node-%d-7-", berlin), fmt.Sprintf("xui-node-%d-9-", paris)
	for tag, want := range map[string][]string{
		"stored": {berlinPrefix}, "legacy": {parisPrefix}, "mixed": {"direct", berlinPrefix},
	} {
		if got := selectorOf(balancerNamed(t, cfg, tag)); !slices.Equal(got, want) {
			t.Errorf("balancer %s selects %v, want %v", tag, got, want)
		}
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
	if fingerprint := svc.expandNodeBalancers(cfg); fingerprint != "" {
		t.Fatalf("a template with no balancer of nodes has the node outbounds %q", fingerprint)
	}
	if string(cfg.RouterConfig) != routing || string(cfg.OutboundConfigs) != outbounds {
		t.Fatal("a template with no balancer of nodes was rewritten")
	}
}

// An agent has no outbound to a node: it keeps what a balancer selects besides nodes, and treats one that selects
// nothing else as the panel does, its rules going to the fallback or the blackhole, which the node says.
func TestAgentConfigTakesTheNodesOutOfItsBalancers(t *testing.T) {
	setupSettingTestDB(t)
	agent := seedTaggedAgent(t, "agent-1")
	berlin := seedTaggedAgent(t, "berlin")
	useNodeOutbounds(t, map[string][]NodeOutbound{"relay@x": {nodeOut(berlin, 7, 11, "berlin.example.com", 443)}})
	withFallback := balancerOfNodes("with-fallback", "random", "relay@x", berlin)
	withFallback["fallbackTag"] = "direct"
	mixed := balancerOfNodes("mixed", "random", "relay@x", berlin)
	mixed["selector"] = []any{"direct", nodeSelector}
	// What the template holds once the last node of a balancer is deleted: the key stays, with no nodes.
	emptied := balancerOfNodes("emptied", "random", "relay@x")
	emptied["selector"] = []any{"direct"}
	plain := map[string]any{"tag": "plain", "selector": []any{"direct"}}
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin), withFallback, mixed, emptied, plain},
		ruleToBalancer("geosite:nodes", "exits"), ruleToBalancer("geosite:again", "exits"), ruleToBalancer("geosite:fallback", "with-fallback"),
		ruleToBalancer("geosite:mixed", "mixed"), ruleToBalancer("geosite:emptied", "emptied"), ruleToBalancer("geosite:plain", "plain"))

	svc := &XrayService{}
	agentCfg, err := svc.GetAgentConfig(agent)
	if err != nil {
		t.Fatal(err)
	}
	coreBuildsBalancers(t, agentCfg)
	coreTakesTheRules(t, agentCfg)
	for _, tag := range []string{"exits", "with-fallback"} {
		if balancerNamed(t, agentCfg, tag) != nil {
			t.Fatalf("balancer %s selects nothing but nodes, so an agent must lose it: %s", tag, agentCfg.RouterConfig)
		}
	}
	for tag, want := range map[string][]string{"mixed": {"direct"}, "emptied": {"direct"}, "plain": {"direct"}} {
		balancer := balancerNamed(t, agentCfg, tag)
		if balancer == nil || !slices.Equal(selectorOf(balancer), want) {
			t.Fatalf("balancer %s must stay on an agent with the selector %v: %s", tag, want, agentCfg.RouterConfig)
		}
		if rule := ruleForDomain(t, agentCfg, "geosite:"+tag); rule["balancerTag"] != tag {
			t.Fatalf("the rule of balancer %s must stay, got %v", tag, rule)
		}
	}
	for _, domain := range []string{"geosite:nodes", "geosite:again"} {
		if rule := ruleForDomain(t, agentCfg, domain); rule["outboundTag"] != missingOutboundTag || rule["balancerTag"] != nil {
			t.Fatalf("a rule that led to a balancer of nodes only must block on an agent, got %v", rule)
		}
	}
	if rule := ruleForDomain(t, agentCfg, "geosite:fallback"); rule["outboundTag"] != "direct" || rule["balancerTag"] != nil {
		t.Fatalf("a rule that led to a balancer of nodes only with a fallback must send to it on an agent, got %v", rule)
	}
	tags := outboundTagList(t, agentCfg)
	if n := slices.Index(tags, missingOutboundTag); n < 0 || slices.Index(tags[n+1:], missingOutboundTag) >= 0 {
		t.Fatalf("want one blackhole %s among %v", missingOutboundTag, tags)
	}
	problem := agentConfigProblem(agent)
	for _, tag := range []string{"exits", "with-fallback"} {
		if strings.Count(problem, fmt.Sprintf("balancer %q", tag)) != 1 {
			t.Fatalf("the node must say once what became of its rules to balancer %s, got %q", tag, problem)
		}
	}
	for _, tag := range []string{"mixed", "emptied", "plain"} {
		if strings.Contains(problem, fmt.Sprintf("balancer %q", tag)) {
			t.Fatalf("balancer %s works on an agent and must not be reported, got %q", tag, problem)
		}
	}
	if strings.Contains(string(agentCfg.RouterConfig), nodeMembersKey) || slices.ContainsFunc(outboundTagList(t, agentCfg), func(tag string) bool { return strings.HasPrefix(tag, nodeOutboundPrefix) }) {
		t.Fatalf("an agent got something of the nodes: %s", agentCfg.RouterConfig)
	}

	panelCfg := renderPanelConfig(t)
	panelRules := map[string]string{"exits": "geosite:nodes", "with-fallback": "geosite:fallback", "mixed": "geosite:mixed", "emptied": "geosite:emptied"}
	for tag, domain := range panelRules {
		if balancerNamed(t, panelCfg, tag) == nil || ruleForDomain(t, panelCfg, domain)["balancerTag"] != tag {
			t.Fatalf("the panel's own core must keep balancer %s and its rule: %s", tag, panelCfg.RouterConfig)
		}
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

// useChangingPort serves the outbound of one node whose port the test can change, and clears what the core was given.
func useChangingPort(t *testing.T, node int, port *int) *XrayService {
	t.Helper()
	SetNodeOutboundSource(func(string, []int) ([]NodeOutbound, error) {
		return []NodeOutbound{nodeOut(node, 7, 11, "berlin.example.com", *port)}, nil
	})
	t.Cleanup(func() { nodeOutboundSource.Store(nil) })
	givenNodeOutbounds.Store("")
	t.Cleanup(func() { givenNodeOutbounds.Store("") })
	svc := &XrayService{}
	svc.IsNeedRestartAndSetFalse()
	t.Cleanup(func() { svc.IsNeedRestartAndSetFalse() })
	return svc
}

func TestRefreshNodeOutboundsAsksForACheckWhenTheNodesChange(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	port := 443
	svc := useChangingPort(t, berlin, &port)

	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("a template with no balancer of nodes asked for a check")
	}

	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))
	giveTheCore(t)
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("nothing changed since the core was given its config, yet a check was asked for")
	}

	port = 8443
	svc.RefreshNodeOutbounds()
	if !svc.IsNeedRestartAndSetFalse() {
		t.Fatal("the node changed a port, but no check was asked for")
	}

	giveTheCore(t)
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("a check was asked for again after the core was given the new config")
	}
}

// The page that shows the config builds one that goes to nothing, and must not make the core look up to date.
func TestShowingTheConfigDoesNotHideAChangeOfTheNodes(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	port := 443
	svc := useChangingPort(t, berlin, &port)
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))
	giveTheCore(t)

	port = 8443
	renderPanelConfig(t)
	svc.RefreshNodeOutbounds()
	if !svc.IsNeedRestartAndSetFalse() {
		t.Fatal("the core still runs the old port, but showing the config made a check seem needless")
	}
}

func TestRefreshNodeOutboundsLeavesACoreThatWasStoppedAlone(t *testing.T) {
	setupSettingTestDB(t)
	berlin := seedTaggedAgent(t, "berlin")
	port := 443
	svc := useChangingPort(t, berlin, &port)
	useBalancers(t, []any{balancerOfNodes("exits", "random", "relay@x", berlin)}, ruleToBalancer("geosite:test", "exits"))
	giveTheCore(t)
	port = 8443

	isManuallyStopped.Store(true)
	t.Cleanup(func() { isManuallyStopped.Store(false) })
	svc.RefreshNodeOutbounds()
	if svc.IsNeedRestartAndSetFalse() {
		t.Fatal("a core the operator stopped was asked to check its config, which would be built again at every tick")
	}
}
