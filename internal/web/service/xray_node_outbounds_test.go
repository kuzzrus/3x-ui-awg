package service

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"

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

func TestAgentConfigIsRefusedWhenARuleSendsToAnOutboundTheNodeLacks(t *testing.T) {
	setupSettingTestDB(t)
	addTemplateOutbounds(t, hop("us-hop", map[string]any{"nodeTags": []any{"us"}}))
	addTemplateRules(t, markedRule("marker-us-only", map[string]any{
		"nodeTags": []any{"eu"}, "outboundTag": "us-hop",
	}))
	eu := seedTaggedAgent(t, "eu-1", "eu")
	both := seedTaggedAgent(t, "both-1", "eu", "us")

	svc := &XrayService{}
	_, err := svc.GetAgentConfig(eu)
	if err == nil || !strings.Contains(err.Error(), `"us-hop"`) || !strings.Contains(err.Error(), "us") {
		t.Fatalf("GetAgentConfig(eu) = %v, want a refusal that names the outbound and the tag that gives it", err)
	}
	cfg, err := svc.GetAgentConfig(both)
	if err != nil {
		t.Fatalf("a node that has the rule and the outbound: %v", err)
	}
	if _, markers := renderedRules(t, cfg); len(markers) != 1 || markers[0] != "us-only" {
		t.Fatalf("rules = %v, want the one that sends to us-hop", markers)
	}
	if _, err := svc.GetXrayConfig(); err != nil {
		t.Fatalf("the panel's own core has neither the rule nor the outbound: %v", err)
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
