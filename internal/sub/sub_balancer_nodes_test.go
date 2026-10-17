package sub

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedSubNode(t *testing.T, name string) *model.Node {
	t.Helper()
	node := &model.Node{Name: name, Address: name + ".example.com", Port: 2053, Scheme: "https", ApiToken: "token"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return node
}

// putOnNode records the inbound as one that the node runs, as the panel stores it.
func putOnNode(t *testing.T, inbound *model.Inbound, node *model.Node) {
	t.Helper()
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", inbound.Id).Update("node_id", node.Id).Error; err != nil {
		t.Fatalf("put inbound %s on node %s: %v", inbound.Tag, node.Name, err)
	}
}

// balancerMemberPorts is the sorted ports of the member outbounds of a balancer document.
func balancerMemberPorts(t *testing.T, doc map[string]any) []int {
	t.Helper()
	var ports []int
	outbounds, _ := doc["outbounds"].([]any)
	for _, raw := range outbounds {
		outbound, _ := raw.(map[string]any)
		if tag, _ := outbound["tag"].(string); !strings.HasPrefix(tag, "bal-") {
			continue
		}
		settings, _ := outbound["settings"].(map[string]any)
		port, ok := settings["port"].(float64)
		if !ok {
			t.Fatalf("member %v has no port in its settings", outbound["tag"])
		}
		ports = append(ports, int(port))
	}
	slices.Sort(ports)
	return ports
}

func subBalancerDocs(t *testing.T, subID string) (map[string]map[string]any, string) {
	t.Helper()
	js := NewSubJsonService("", "", "", "", NewSubService(""))
	out, _, err := js.GetJson(subID, "req.example.com", true)
	if err != nil {
		t.Fatalf("GetJson: %v", err)
	}
	byRemarks := map[string]map[string]any{}
	if strings.TrimSpace(out) != "" {
		for _, doc := range parseSubJsonDocs(t, out) {
			byRemarks[doc["remarks"].(string)] = doc
		}
	}
	return byRemarks, out
}

// A node stands for the inbounds the subscriber has on it: two on Berlin, one on
// Paris and one on the panel itself, and each balancer gets the ones it names.
func TestSubJson_BalancerNodeMembers(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	paris := seedSubNode(t, "paris")
	berlinA := seedSubInbound(t, "s1", "berlin-a", 4801, 1, wsTLSStream)
	berlinB := seedSubInbound(t, "s1", "berlin-b", 4802, 2, wsTLSStream)
	parisA := seedSubInbound(t, "s1", "paris-a", 4803, 3, wsTLSStream)
	local := seedSubInbound(t, "s1", "local", 4804, 4, wsTLSStream)
	putOnNode(t, berlinA, berlin)
	putOnNode(t, berlinB, berlin)
	putOnNode(t, parisA, paris)

	seedSubBalancer(t, &model.SubBalancer{Remark: "berlin", Strategy: "random", NodeIds: []int{berlin.Id}, SortOrder: 1, Enabled: true})
	seedSubBalancer(t, &model.SubBalancer{Remark: "both", Strategy: "random", NodeIds: []int{berlin.Id, paris.Id}, SortOrder: 2, Enabled: true})
	seedSubBalancer(t, &model.SubBalancer{Remark: "mixed", Strategy: "random", InboundIds: []int{local.Id}, NodeIds: []int{paris.Id}, SortOrder: 3, Enabled: true})

	docs, out := subBalancerDocs(t, "s1")
	for remarks, want := range map[string][]int{
		"berlin": {4801, 4802},
		"both":   {4801, 4802, 4803},
		"mixed":  {4803, 4804},
	} {
		doc := docs[remarks]
		if doc == nil {
			t.Fatalf("balancer %q missing:\n%s", remarks, out)
		}
		if got := balancerMemberPorts(t, doc); !slices.Equal(got, want) {
			t.Fatalf("balancer %q members = %v, want %v", remarks, got, want)
		}
	}
}

// An inbound that is picked on its own and also runs on a picked node is one
// member, not two.
func TestSubJson_BalancerNodeAndInboundAreOneMember(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	first := seedSubInbound(t, "s1", "first", 4811, 1, wsTLSStream)
	second := seedSubInbound(t, "s1", "second", 4812, 2, wsTLSStream)
	putOnNode(t, first, berlin)
	putOnNode(t, second, berlin)
	balancer := seedSubBalancer(t, &model.SubBalancer{
		Remark: "once", Strategy: "random", InboundIds: []int{first.Id}, NodeIds: []int{berlin.Id}, SortOrder: 1, Enabled: true,
	})

	docs, out := subBalancerDocs(t, "s1")
	doc := docs["once"]
	if doc == nil {
		t.Fatalf("balancer missing:\n%s", out)
	}
	prefix := fmt.Sprintf("bal-%d-", balancer.Id)
	if tags, want := docOutboundTags(doc), prefix+"vless,"+prefix+"vless-2,direct,block"; strings.Join(tags, ",") != want {
		t.Fatalf("outbound tags = %v, want %s (two members and no third)", tags, want)
	}
}

// The inbounds of a node follow the rules of the ones picked one by one: one that is off, or that
// the subscriber has no client on, is no member, and a balancer left with none is not emitted.
func TestSubJson_BalancerNodeSkipsOffAndForeignInbounds(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	paris := seedSubNode(t, "paris")
	live := seedSubInbound(t, "s1", "live", 4821, 1, wsTLSStream)
	off := seedSubInbound(t, "s1", "off", 4822, 2, wsTLSStream)
	foreign := seedSubInbound(t, "other", "foreign", 4823, 3, wsTLSStream)
	onlyForeign := seedSubInbound(t, "other", "only-foreign", 4824, 4, wsTLSStream)
	putOnNode(t, live, berlin)
	putOnNode(t, off, berlin)
	putOnNode(t, foreign, berlin)
	putOnNode(t, onlyForeign, paris)
	if err := database.GetDB().Model(&model.Inbound{}).Where("id = ?", off.Id).Update("enable", false).Error; err != nil {
		t.Fatalf("disable inbound: %v", err)
	}

	seedSubBalancer(t, &model.SubBalancer{Remark: "berlin", Strategy: "random", NodeIds: []int{berlin.Id}, SortOrder: 1, Enabled: true})
	seedSubBalancer(t, &model.SubBalancer{Remark: "paris", Strategy: "random", NodeIds: []int{paris.Id}, SortOrder: 2, Enabled: true})

	docs, out := subBalancerDocs(t, "s1")
	berlinDoc := docs["berlin"]
	if berlinDoc == nil {
		t.Fatalf("balancer berlin missing:\n%s", out)
	}
	if got := balancerMemberPorts(t, berlinDoc); !slices.Equal(got, []int{4821}) {
		t.Fatalf("berlin members = %v, want only the live inbound [4821]", got)
	}
	if docs["paris"] != nil {
		t.Fatalf("balancer paris has no inbound of this subscriber and must not be emitted:\n%s", out)
	}
}

// A balancer that picks no node keeps choosing by inbound alone, so an inbound
// that happens to run on some node is not pulled in.
func TestSubJson_BalancerWithoutNodesIgnoresNodeInbounds(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	picked := seedSubInbound(t, "s1", "picked", 4831, 1, wsTLSStream)
	sibling := seedSubInbound(t, "s1", "sibling", 4832, 2, wsTLSStream)
	putOnNode(t, picked, berlin)
	putOnNode(t, sibling, berlin)
	seedSubBalancer(t, &model.SubBalancer{Remark: "one", Strategy: "random", InboundIds: []int{picked.Id}, SortOrder: 1, Enabled: true})

	docs, out := subBalancerDocs(t, "s1")
	doc := docs["one"]
	if doc == nil {
		t.Fatalf("balancer missing:\n%s", out)
	}
	if got := balancerMemberPorts(t, doc); !slices.Equal(got, []int{4831}) {
		t.Fatalf("members = %v, want only the inbound that was picked [4831]", got)
	}
}

// The weight of an inbound picked one by one still counts when its node is picked
// as well; the other inbounds of the node weigh the default.
func TestSubJson_BalancerNodeKeepsWeightOfPickedInbound(t *testing.T) {
	seedSubDB(t)
	berlin := seedSubNode(t, "berlin")
	heavy := seedSubInbound(t, "s1", "heavy", 4841, 1, wsTLSStream)
	plain := seedSubInbound(t, "s1", "plain", 4842, 2, wsTLSStream)
	putOnNode(t, heavy, berlin)
	putOnNode(t, plain, berlin)
	balancer := seedSubBalancer(t, &model.SubBalancer{
		Remark: "weighted", Strategy: "leastLoad", InboundIds: []int{heavy.Id}, NodeIds: []int{berlin.Id},
		MemberWeights: map[int]float64{heavy.Id: 2.5}, SortOrder: 1, Enabled: true,
	})

	docs, out := subBalancerDocs(t, "s1")
	doc := docs["weighted"]
	if doc == nil {
		t.Fatalf("balancer missing:\n%s", out)
	}
	routing, _ := doc["routing"].(map[string]any)
	balancers, _ := routing["balancers"].([]any)
	strategy, _ := balancers[0].(map[string]any)["strategy"].(map[string]any)
	settings, _ := strategy["settings"].(map[string]any)
	costs, _ := settings["costs"].([]any)
	got := map[string]float64{}
	for _, raw := range costs {
		cost, _ := raw.(map[string]any)
		got[cost["match"].(string)], _ = cost["value"].(float64)
	}
	prefix := fmt.Sprintf("^bal-%d-", balancer.Id)
	want := map[string]float64{prefix + "vless$": 2.5, prefix + "vless-2$": 1}
	if len(got) != len(want) {
		t.Fatalf("costs = %v, want %v", got, want)
	}
	for match, value := range want {
		if got[match] != value {
			t.Fatalf("costs = %v, want %v", got, want)
		}
	}
}

// A 0 in the nodes of a balancer, which only a row edited by hand can hold, does
// not stand for the panel itself: its own inbounds are not members.
func TestSubJson_BalancerNodeZeroIsNotThePanel(t *testing.T) {
	seedSubDB(t)
	seedSubInbound(t, "s1", "own", 4851, 1, wsTLSStream)
	seedSubBalancer(t, &model.SubBalancer{Remark: "zero", Strategy: "random", NodeIds: []int{0}, SortOrder: 1, Enabled: true})

	docs, out := subBalancerDocs(t, "s1")
	if docs["zero"] != nil {
		t.Fatalf("a balancer of node 0 must not be emitted:\n%s", out)
	}
}
