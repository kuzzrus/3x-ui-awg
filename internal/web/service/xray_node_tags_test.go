package service

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// markedRule is a routing rule that tells itself apart in a rendered config by its domain.
func markedRule(marker string, extra map[string]any) map[string]any {
	rule := map[string]any{"type": "field", "domain": []any{marker}, "outboundTag": "direct"}
	maps.Copy(rule, extra)
	return rule
}

// addTemplateRules appends rules to the routing section of the stored template.
func addTemplateRules(t *testing.T, rules ...map[string]any) {
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
	existing, _ := routing["rules"].([]any)
	for _, rule := range rules {
		existing = append(existing, rule)
	}
	routing["rules"] = existing
	parsed["routing"] = routing
	raw, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(raw)); err != nil {
		t.Fatal(err)
	}
}

// renderedRules is the rules of a config, with the markers of the ones the test added.
func renderedRules(t *testing.T, cfg *xray.Config) (rules []map[string]any, markers []string) {
	t.Helper()
	var routing struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	for _, rule := range routing.Rules {
		if _, leaked := rule["nodeTags"]; leaked {
			t.Errorf("a rendered rule still carries the panel-only nodeTags: %v", rule)
		}
		domains, _ := rule["domain"].([]any)
		if len(domains) == 1 {
			if marker, _ := domains[0].(string); strings.HasPrefix(marker, "marker-") {
				markers = append(markers, strings.TrimPrefix(marker, "marker-"))
			}
		}
	}
	return routing.Rules, markers
}

func seedTaggedAgent(t *testing.T, name string, tags ...string) int {
	t.Helper()
	node := &model.Node{Name: name, Kind: model.NodeKindAgent, Address: "192.0.2.10", Port: 8443, Enable: true, Tags: tags}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return node.Id
}

func TestAgentConfigRunsTheRulesItsNodesTagsPick(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateRules(t,
		markedRule("marker-all", nil),
		markedRule("marker-eu", map[string]any{"nodeTags": []any{"eu"}}),
		markedRule("marker-us", map[string]any{"nodeTags": []any{"us"}}),
		markedRule("marker-either", map[string]any{"nodeTags": []any{"us", "eu"}}),
		markedRule("marker-case", map[string]any{"nodeTags": []any{"EU"}}),
		markedRule("marker-off", map[string]any{"nodeTags": []any{"eu"}, "enabled": false}),
		markedRule("marker-empty", map[string]any{"nodeTags": []any{}}),
	)
	eu := seedTaggedAgent(t, "eu-1", "eu", "exit")
	us := seedTaggedAgent(t, "us-1", "us")
	bare := seedTaggedAgent(t, "bare")

	svc := &XrayService{}
	for _, tc := range []struct {
		name string
		id   int
		want []string
	}{
		{"a node tagged eu", eu, []string{"all", "eu", "either", "case", "empty"}},
		{"a node tagged us", us, []string{"all", "us", "either", "empty"}},
		{"a node without tags", bare, []string{"all", "empty"}},
	} {
		cfg, err := svc.GetAgentConfig(tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if _, got := renderedRules(t, cfg); !slices.Equal(got, tc.want) {
			t.Fatalf("%s runs the rules %v, want %v", tc.name, got, tc.want)
		}
	}

	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, got := renderedRules(t, master); !slices.Equal(got, []string{"all", "empty"}) {
		t.Fatalf("the panel's own core runs the rules %v, want only the ones that are for every core", got)
	}
}

func TestRuleNodeTagsNeverDropTheApiRule(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateRules(t, map[string]any{
		"type": "field", "inboundTag": []any{"api"}, "outboundTag": "api", "nodeTags": []any{"nowhere"},
	})
	node := seedTaggedAgent(t, "agent", "eu")

	svc := &XrayService{}
	agent, err := svc.GetAgentConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*xray.Config{"agent": agent, "master": master} {
		rules, _ := renderedRules(t, cfg)
		kept := 0
		for _, rule := range rules {
			if isApiRule(rule) {
				kept++
			}
		}
		if kept < 2 {
			t.Fatalf("the %s config holds %d api rules, want the template's and the one with nodeTags, which stats need whatever they say", name, kept)
		}
	}
}

func TestARuleWithUnreadableNodeTagsIsInNoConfig(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateRules(t,
		markedRule("marker-string", map[string]any{"nodeTags": "eu"}),
		markedRule("marker-number", map[string]any{"nodeTags": []any{1}}),
		markedRule("marker-blank", map[string]any{"nodeTags": []any{" "}}),
		markedRule("marker-ok", nil),
	)
	node := seedTaggedAgent(t, "agent", "eu")

	svc := &XrayService{}
	agent, err := svc.GetAgentConfig(node)
	if err != nil {
		t.Fatal(err)
	}
	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	for name, cfg := range map[string]*xray.Config{"agent": agent, "master": master} {
		if _, got := renderedRules(t, cfg); !slices.Equal(got, []string{"ok"}) {
			t.Fatalf("the %s config runs the rules %v, want a rule that cannot say who it is for left out", name, got)
		}
	}
}

func TestAgentConfigOfAMissingNodeIsAnError(t *testing.T) {
	setupSettingTestDB(t)
	if _, err := (&XrayService{}).GetAgentConfig(4242); err == nil {
		t.Fatal("a config was rendered for a node that does not exist")
	}
}

func TestSavingTheTemplateRefusesNodeTagsThatAreNotTags(t *testing.T) {
	svc := &XraySettingService{}
	template := func(nodeTags string) string {
		return `{"routing":{"rules":[{"type":"field","outboundTag":"direct"},{"type":"field","outboundTag":"direct","nodeTags":` + nodeTags + `}]}}`
	}
	for name, tc := range map[string]struct {
		nodeTags string
		bad      bool
	}{
		"a list of tags": {`["eu","exit"]`, false},
		"an empty list":  {`[]`, false},
		"a string":       {`"eu"`, true},
		"a number":       {`[1]`, true},
		"a blank tag":    {`[""]`, true},
		"a long tag":     {`["` + strings.Repeat("a", maxNodeTagLen+1) + `"]`, true},
		"null":           {`null`, true},
	} {
		err := svc.CheckXrayConfig(template(tc.nodeTags))
		switch {
		case tc.bad && (err == nil || !strings.Contains(err.Error(), "routing rule 2")):
			t.Errorf("%s: CheckXrayConfig = %v, want a refusal that names rule 2", name, err)
		case !tc.bad && err != nil:
			t.Errorf("%s: CheckXrayConfig = %v, want it accepted", name, err)
		}
	}
}
