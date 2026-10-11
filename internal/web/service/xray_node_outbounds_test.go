package service

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// addTemplateOutbounds appends outbounds to the stored template.
func addTemplateOutbounds(t *testing.T, outbounds ...map[string]any) {
	t.Helper()
	settings := &SettingService{}
	template, err := settings.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(template), &parsed); err != nil {
		t.Fatal(err)
	}
	existing, _ := parsed["outbounds"].([]any)
	for _, outbound := range outbounds {
		existing = append(existing, outbound)
	}
	parsed["outbounds"] = existing
	raw, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(raw)); err != nil {
		t.Fatal(err)
	}
}

func hop(tag string, extra map[string]any) map[string]any {
	outbound := map[string]any{"tag": tag, "protocol": "freedom"}
	maps.Copy(outbound, extra)
	return outbound
}

// outboundTagsOf is the tags of the outbounds of a config, which must not carry the panel-only key.
func outboundTagsOf(t *testing.T, cfg *xray.Config) map[string]bool {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(cfg.OutboundConfigs, &list); err != nil {
		t.Fatal(err)
	}
	tags := map[string]bool{}
	for _, outbound := range list {
		if _, leaked := outbound["nodeTags"]; leaked {
			t.Errorf("a rendered outbound still carries the panel-only nodeTags: %v", outbound)
		}
		tag, _ := outbound["tag"].(string)
		tags[tag] = true
	}
	return tags
}

func TestAgentConfigHasTheOutboundsItsNodesTagsPick(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t,
		hop("eu-hop", map[string]any{"nodeTags": []any{"eu"}}),
		hop("us-hop", map[string]any{"nodeTags": []any{"US"}}),
		hop("any-hop", nil),
		hop("empty-hop", map[string]any{"nodeTags": []any{}}),
	)
	eu := seedTaggedAgent(t, "eu-1", "eu")
	us := seedTaggedAgent(t, "us-1", "us")
	bare := seedTaggedAgent(t, "bare")

	svc := &XrayService{}
	for _, tc := range []struct {
		name string
		id   int
		want map[string]bool
	}{
		{"a node tagged eu", eu, map[string]bool{"eu-hop": true, "us-hop": false, "any-hop": true, "empty-hop": true}},
		{"a node tagged us, whatever the case", us, map[string]bool{"eu-hop": false, "us-hop": true, "any-hop": true, "empty-hop": true}},
		{"a node without tags", bare, map[string]bool{"eu-hop": false, "us-hop": false, "any-hop": true, "empty-hop": true}},
	} {
		cfg, err := svc.GetAgentConfig(tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		have := outboundTagsOf(t, cfg)
		for tag, want := range tc.want {
			if have[tag] != want {
				t.Errorf("%s has outbound %s = %v, want %v", tc.name, tag, have[tag], want)
			}
		}
		if !have["direct"] {
			t.Errorf("%s lost the template's own outbounds: %v", tc.name, have)
		}
	}

	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	have := outboundTagsOf(t, master)
	if have["eu-hop"] || have["us-hop"] || !have["any-hop"] || !have["empty-hop"] || !have["direct"] {
		t.Fatalf("the panel's own core has the outbounds %v, want the ones for every core and none of the nodes'", have)
	}
}

// addTemplateBalancers appends balancers to the routing section of the stored template.
func addTemplateBalancers(t *testing.T, balancers ...map[string]any) {
	t.Helper()
	settings := &SettingService{}
	template, err := settings.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(template), &parsed); err != nil {
		t.Fatal(err)
	}
	routing, _ := parsed["routing"].(map[string]any)
	if routing == nil {
		routing = map[string]any{}
	}
	existing, _ := routing["balancers"].([]any)
	for _, balancer := range balancers {
		existing = append(existing, balancer)
	}
	routing["balancers"] = existing
	parsed["routing"] = routing
	raw, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(raw)); err != nil {
		t.Fatal(err)
	}
}

// ruleTargets is where each rule of a config sends, by the marker of the rules the test added.
func ruleTargets(t *testing.T, cfg *xray.Config) map[string]string {
	t.Helper()
	rules, _ := renderedRules(t, cfg)
	targets := map[string]string{}
	for _, rule := range rules {
		domains, _ := rule["domain"].([]any)
		if len(domains) != 1 {
			continue
		}
		marker, _ := domains[0].(string)
		if !strings.HasPrefix(marker, "marker-") {
			continue
		}
		if tag, _ := rule["outboundTag"].(string); tag != "" {
			targets[strings.TrimPrefix(marker, "marker-")] = "outbound " + tag
		}
		if tag, _ := rule["balancerTag"].(string); tag != "" {
			targets[strings.TrimPrefix(marker, "marker-")] = "balancer " + tag
		}
	}
	return targets
}

func TestAgentConfigBlocksARuleToAnOutboundTheNodeLacks(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}))
	addTemplateRules(t, markedRule("marker-us-only", map[string]any{
		"nodeTags": []any{"eu"}, "outboundTag": "us-hop",
	}))
	eu := seedTaggedAgent(t, "eu-1", "eu")
	both := seedTaggedAgent(t, "both-1", "eu", "us")
	t.Cleanup(func() { setAgentConfigProblem(eu, ""); setAgentConfigProblem(both, "") })

	// A refusal would keep the node on its last config, and so keep serving the clients that were
	// removed since: the node gets the whole config, with the traffic of the rule blocked.
	svc := &XrayService{}
	cfg, err := svc.GetAgentConfig(eu)
	if err != nil {
		t.Fatalf("GetAgentConfig(eu): %v", err)
	}
	if got := ruleTargets(t, cfg)["us-only"]; got != "outbound "+missingOutboundTag {
		t.Fatalf("the rule sends to %q, want the blackhole", got)
	}
	var blackholes int
	for tag, ok := range outboundTagsOf(t, cfg) {
		if ok && tag == missingOutboundTag {
			blackholes++
		}
	}
	if blackholes != 1 {
		t.Fatalf("the config has %d outbounds tagged %s, want the blackhole the rule goes to", blackholes, missingOutboundTag)
	}
	problem := agentConfigProblem(eu)
	if !strings.Contains(problem, `"us-hop"`) || !strings.Contains(problem, "tagged us") {
		t.Fatalf("problem = %q, want it to say which outbound is missing and which tags give it", problem)
	}

	cfg, err = svc.GetAgentConfig(both)
	if err != nil {
		t.Fatalf("a node that has the rule and the outbound: %v", err)
	}
	if got := ruleTargets(t, cfg)["us-only"]; got != "outbound us-hop" {
		t.Fatalf("the rule of a node that has the outbound sends to %q, want us-hop", got)
	}
	if outboundTagsOf(t, cfg)[missingOutboundTag] || agentConfigProblem(both) != "" {
		t.Fatal("a node that has everything its rules name got a blackhole or a problem")
	}
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatalf("the panel's own core has neither the rule nor the outbound: %v", err)
	}
}

// A balancer left without outbounds sends its traffic out of the default one, so the rules that use it are
// blocked, and a fallback to an outbound the node lacks is dropped like the panel's own cleanup does.
func TestAgentConfigRepairsABalancerTheNodeLacksTheOutboundsOf(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}), hop("eu-hop", map[string]any{"nodeTags": []any{"eu"}}))
	addTemplateBalancers(t,
		map[string]any{"tag": "us-pool", "selector": []any{"us-"}, "fallbackTag": "us-hop"},
		map[string]any{"tag": "eu-pool", "selector": []any{"eu-", "direct"}, "fallbackTag": "eu-hop"},
	)
	addTemplateRules(t,
		map[string]any{"type": "field", "domain": []any{"marker-us-pool"}, "balancerTag": "us-pool"},
		map[string]any{"type": "field", "domain": []any{"marker-eu-pool"}, "balancerTag": "eu-pool"},
	)
	eu := seedTaggedAgent(t, "eu-1", "eu")
	us := seedTaggedAgent(t, "us-1", "us")
	t.Cleanup(func() { setAgentConfigProblem(eu, ""); setAgentConfigProblem(us, "") })

	balancerFallbacks := func(cfg *xray.Config) map[string]string {
		var routing struct {
			Balancers []struct {
				Tag         string `json:"tag"`
				FallbackTag string `json:"fallbackTag"`
			} `json:"balancers"`
		}
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, b := range routing.Balancers {
			out[b.Tag] = b.FallbackTag
		}
		return out
	}

	svc := &XrayService{}
	cfg, err := svc.GetAgentConfig(eu)
	if err != nil {
		t.Fatal(err)
	}
	targets := ruleTargets(t, cfg)
	if targets["us-pool"] != "outbound "+missingOutboundTag {
		t.Fatalf("the rule of the emptied balancer sends to %q, want the blackhole", targets["us-pool"])
	}
	if targets["eu-pool"] != "balancer eu-pool" {
		t.Fatalf("the rule of a balancer that still has outbounds sends to %q, want it left alone", targets["eu-pool"])
	}
	fallbacks := balancerFallbacks(cfg)
	if fallbacks["us-pool"] != "" || fallbacks["eu-pool"] != "eu-hop" {
		t.Fatalf("fallbacks = %v, want the one that is gone dropped and the one the node has kept", fallbacks)
	}
	if problem := agentConfigProblem(eu); !strings.Contains(problem, "us-pool") || !strings.Contains(problem, "falls back") {
		t.Fatalf("problem = %q, want the balancer and the fallback named", problem)
	}

	cfg, err = svc.GetAgentConfig(us)
	if err != nil {
		t.Fatal(err)
	}
	if got := balancerFallbacks(cfg)["us-pool"]; got != "us-hop" {
		t.Fatalf("a node that has us-hop lost its fallback: %q", got)
	}
	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("the panel's own core has no node-only outbound at all: %v", err)
	}
	if got := balancerFallbacks(master); got["us-pool"] != "" || got["eu-pool"] != "" {
		t.Fatalf("the panel's own core kept fallbacks %v to outbounds it does not have, and would not start", got)
	}
}

// Templates in the field hold rules for outbounds that were deleted long ago, and rendering them
// has to go on as it always did.
func TestARuleToAnOutboundThatIsNowhereStillRenders(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}))
	addTemplateRules(t, markedRule("marker-ghost", map[string]any{"outboundTag": "deleted-long-ago"}))
	node := seedTaggedAgent(t, "bare")

	svc := &XrayService{}
	if _, err := svc.GetAgentConfig(node); err != nil {
		t.Fatalf("GetAgentConfig: %v", err)
	}
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
}

func TestSavingTheTemplateChecksTheNodeTagsOfOutbounds(t *testing.T) {
	setupSettingTestDB(t)
	svc := &XraySettingService{}
	template := func(outbound, rule string) string {
		return `{"outbounds":[{"tag":"direct","protocol":"freedom"},` + outbound + `],` +
			`"routing":{"rules":[` + rule + `]}}`
	}
	nodeOnly := `{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu","Exit"]}`
	toIt := func(extra string) string {
		return `{"type":"field","outboundTag":"eu-hop"` + extra + `}`
	}
	for name, tc := range map[string]struct {
		outbound, rule string
		bad            string
	}{
		"a list of tags":                      {nodeOnly, toIt(`,"nodeTags":["eu"]`), ""},
		"outbound tags that are no list":      {`{"tag":"eu-hop","protocol":"freedom","nodeTags":"eu"}`, toIt(``), "outbound 2"},
		"an outbound tag that is blank":       {`{"tag":"eu-hop","protocol":"freedom","nodeTags":[" "]}`, toIt(``), "outbound 2"},
		"a shared rule to a node-only hop":    {nodeOnly, toIt(``), "routing rule 1 is for every core"},
		"an empty list is shared as well":     {nodeOnly, toIt(`,"nodeTags":[]`), "routing rule 1 is for every core"},
		"a disabled shared rule is no rule":   {nodeOnly, toIt(`,"enabled":false`), ""},
		"a shared rule to a shared hop":       {`{"tag":"eu-hop","protocol":"freedom"}`, toIt(``), ""},
		"a rule of other nodes to a node hop": {nodeOnly, toIt(`,"nodeTags":["us"]`), ""},
	} {
		err := svc.CheckXrayConfig(template(tc.outbound, tc.rule))
		switch {
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: CheckXrayConfig = %v, want a refusal that says %q", name, err, tc.bad)
		case tc.bad == "" && err != nil:
			t.Errorf("%s: CheckXrayConfig = %v, want it accepted", name, err)
		}
	}
	err := svc.CheckXrayConfig(template(nodeOnly, toIt(``)))
	if err == nil || !strings.Contains(err.Error(), "eu, Exit") {
		t.Errorf("the refusal of a shared rule = %v, want it to list the tags that give the outbound", err)
	}
}

func TestSavingTheTemplateKeepsTheDefaultOutboundForEveryCore(t *testing.T) {
	svc := &XraySettingService{}
	first := `{"outbounds":[{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]},{"tag":"direct","protocol":"freedom"}]}`
	if err := svc.CheckXrayConfig(first); err == nil || !strings.Contains(err.Error(), "outbound 1 is the default outbound of every core") {
		t.Fatalf("CheckXrayConfig = %v, want a refusal of nodeTags on the first outbound, which the cores without them would lose", err)
	}
	later := `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]}]}`
	if err := svc.CheckXrayConfig(later); err != nil {
		t.Fatalf("CheckXrayConfig = %v, want nodeTags accepted on an outbound that is not the first", err)
	}
}

func TestSavingTheTemplateKeepsChainsOfSharedOutboundsOnSharedOnes(t *testing.T) {
	setupSettingTestDB(t)
	svc := &XraySettingService{}
	template := func(chainExtra string) string {
		return `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]},` +
			`{"tag":"chain","protocol":"freedom"` + chainExtra + `,"streamSettings":{"sockopt":{"dialerProxy":"eu-hop"}}}]}`
	}
	if err := svc.CheckXrayConfig(template(``)); err == nil || !strings.Contains(err.Error(), `outbound 3 is for every core but is chained through outbound "eu-hop"`) {
		t.Fatalf("CheckXrayConfig = %v, want a refusal of a shared outbound chained through a node-only one", err)
	}
	if err := svc.CheckXrayConfig(template(`,"nodeTags":["eu"]`)); err != nil {
		t.Fatalf("CheckXrayConfig = %v, want a chain between outbounds of nodes accepted", err)
	}
}

func TestSavingTheTemplateKeepsOutboundsThePanelUsesForEveryCore(t *testing.T) {
	setupSettingTestDB(t)
	settings := &SettingService{}
	svc := &XraySettingService{}
	limited := `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]}]}`
	if err := svc.CheckXrayConfig(limited); err != nil {
		t.Fatalf("setup: CheckXrayConfig = %v, want it accepted while nothing uses eu-hop", err)
	}

	if err := settings.SetPanelOutbound("eu-hop"); err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckXrayConfig(limited); err == nil || !strings.Contains(err.Error(), "the panel outbound") {
		t.Fatalf("CheckXrayConfig = %v, want a refusal that names the panel outbound", err)
	}
	if err := settings.SetPanelOutbound(""); err != nil {
		t.Fatal(err)
	}

	if err := database.GetDB().Create(&model.Node{Name: "far", Address: "192.0.2.20", Port: 2053, OutboundTag: "eu-hop", Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckXrayConfig(limited); err == nil || !strings.Contains(err.Error(), `the node "far"`) {
		t.Fatalf("CheckXrayConfig = %v, want a refusal that names the node that is reached through it", err)
	}
	if err := database.GetDB().Model(model.Node{}).Where("name = ?", "far").Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}

	bridge := seedInboundOn(t, nil, "mt-in", model.MTProto, true, nil)
	egress := `{"routeThroughXray":true,"routeXrayPort":10808,"outboundTag":"eu-hop"}`
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", bridge.Id).Update("settings", egress).Error; err != nil {
		t.Fatal(err)
	}
	if err := svc.CheckXrayConfig(limited); err == nil || !strings.Contains(err.Error(), `the inbound "mt-in"`) {
		t.Fatalf("CheckXrayConfig = %v, want a refusal that names the inbound whose bridge goes through it", err)
	}
}

func TestAgentConfigSaysWhatIsChainedThroughAnOutboundTheNodeLacks(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t,
		hop("us-hop", map[string]any{"nodeTags": []any{"us"}}),
		hop("chain-eu", map[string]any{
			"nodeTags":       []any{"eu"},
			"streamSettings": map[string]any{"sockopt": map[string]any{"dialerProxy": "us-hop"}},
		}),
	)
	eu := seedTaggedAgent(t, "eu-1", "eu")
	both := seedTaggedAgent(t, "both-1", "eu", "us")
	t.Cleanup(func() { setAgentConfigProblem(eu, ""); setAgentConfigProblem(both, "") })

	svc := &XrayService{}
	cfg, err := svc.GetAgentConfig(eu)
	if err != nil {
		t.Fatal(err)
	}
	problem := agentConfigProblem(eu)
	if !strings.Contains(problem, `"chain-eu" is chained through outbound "us-hop"`) || !strings.Contains(problem, "tagged us") {
		t.Fatalf("problem = %q, want it to say which outbound is chained through which, and who has the second", problem)
	}
	if !strings.Contains(string(cfg.OutboundConfigs), `"dialerProxy":"us-hop"`) {
		t.Fatal("the chain was changed, but it fails closed by itself and is only reported")
	}
	if _, err := svc.GetAgentConfig(both); err != nil || agentConfigProblem(both) != "" {
		t.Fatalf("a node that has both outbounds: %v, problem %q", err, agentConfigProblem(both))
	}
}

// A balancer of outbounds that only some agent nodes have is empty on the panel's own core, and the core
// sends what a rule hands such a balancer out of the default outbound.
func TestSavingTheTemplateKeepsSharedRulesOffBalancersOfNodeOnlyOutbounds(t *testing.T) {
	setupSettingTestDB(t)
	svc := &XraySettingService{}
	template := func(rule string) string {
		return `{"outbounds":[{"tag":"direct","protocol":"freedom"},` +
			`{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]},` +
			`{"tag":"us-hop","protocol":"freedom","nodeTags":["us"]},` +
			`{"tag":"shared-hop","protocol":"freedom"}],` +
			`"routing":{"balancers":[` +
			`{"tag":"hops","selector":["eu-hop","us-hop"]},` +
			`{"tag":"mixed","selector":["eu-hop","shared-hop"]},` +
			`{"tag":"stale","selector":["nothing-"]}],` +
			`"rules":[` + rule + `]}}`
	}
	to := func(balancer, extra string) string {
		return `{"type":"field","balancerTag":"` + balancer + `"` + extra + `}`
	}
	for name, tc := range map[string]struct {
		rule string
		bad  string
	}{
		"a shared rule to a balancer of node-only outbounds": {to("hops", ""), `routing rule 1 is for every core but sends to balancer "hops", whose outbounds only the agent nodes tagged eu, us have`},
		"a rule of nodes to the same balancer":               {to("hops", `,"nodeTags":["eu"]`), ""},
		"a disabled shared rule":                             {to("hops", `,"enabled":false`), ""},
		"a shared rule to a balancer that has a shared one":  {to("mixed", ""), ""},
		"a shared rule to a balancer that selects nothing":   {to("stale", ""), ""},
	} {
		err := svc.CheckXrayConfig(template(tc.rule))
		switch {
		case tc.bad != "" && (err == nil || !strings.Contains(err.Error(), tc.bad)):
			t.Errorf("%s: CheckXrayConfig = %v, want a refusal that says %q", name, err, tc.bad)
		case tc.bad == "" && err != nil:
			t.Errorf("%s: CheckXrayConfig = %v, want it accepted", name, err)
		}
	}
}

func TestBalancersLimitedToNodesCountsTheOutboundsEveryCoreHasBesides(t *testing.T) {
	router := json_util.RawMessage(`{"balancers":[{"tag":"pool","selector":["eu-","sub-"]}]}`)
	list := []map[string]any{
		{"tag": "eu-hop", "protocol": "freedom", "nodeTags": []any{"eu"}},
		{"tag": "eu-two", "protocol": "freedom", "nodeTags": []any{"EU", "x"}},
	}
	got := balancersLimitedToNodes(router, list, nil)
	if tags, ok := got["pool"]; !ok || len(tags) != 3 {
		t.Fatalf("limited = %v, want the pool, which only agent nodes can fill", got)
	}
	if got := balancersLimitedToNodes(router, list, []string{"sub-1"}); len(got) != 0 {
		t.Fatalf("limited = %v, want none: every core has the outbounds of a subscription", got)
	}
}

func TestSavingTheTemplateKeepsWhatThePanelUsesOutOfBalancersOfNodeOnlyOutbounds(t *testing.T) {
	setupSettingTestDB(t)
	settings := &SettingService{}
	svc := &XraySettingService{}
	limited := `{"outbounds":[{"tag":"direct","protocol":"freedom"},{"tag":"eu-hop","protocol":"freedom","nodeTags":["eu"]}],` +
		`"routing":{"balancers":[{"tag":"hops","selector":["eu-"]}]}`
	if err := svc.CheckXrayConfig(limited + `}`); err != nil {
		t.Fatalf("setup: CheckXrayConfig = %v, want it accepted while nothing uses the balancer", err)
	}

	if err := settings.SetPanelOutbound("hops"); err != nil {
		t.Fatal(err)
	}
	err := svc.CheckXrayConfig(limited + `}`)
	if err == nil || !strings.Contains(err.Error(), `balancer "hops" is used by the panel outbound`) {
		t.Fatalf("CheckXrayConfig = %v, want a refusal that names the balancer and the panel outbound", err)
	}
	if err := settings.SetPanelOutbound(""); err != nil {
		t.Fatal(err)
	}

	geodata := limited + `,"geodata":{"outbound":"eu-hop"}}`
	if err := svc.CheckXrayConfig(geodata); err == nil || !strings.Contains(err.Error(), `outbound "eu-hop" is used by the geodata outbound`) {
		t.Fatalf("CheckXrayConfig = %v, want a refusal of a geodata outbound that the panel's own core lacks", err)
	}
}
