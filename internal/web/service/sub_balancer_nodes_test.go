package service

import (
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func seedBalancerNode(t *testing.T, name string) *model.Node {
	t.Helper()
	node := &model.Node{Name: name, Address: name + ".example.com", Port: 2053, Scheme: "https", ApiToken: "token"}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("seed node %s: %v", name, err)
	}
	return node
}

// A node alone is a member list: no inbound has to be picked, the ids are kept
// in the order given with repeats dropped, and Update replaces them.
func TestSubBalancerServiceNodeMembers(t *testing.T) {
	setupSubBalancerDB(t)
	svc := &SubBalancerService{}
	first := seedBalancerNode(t, "first")
	second := seedBalancerNode(t, "second")
	third := seedBalancerNode(t, "third")

	created, err := svc.Create(&model.SubBalancer{
		Remark: "by-node", Strategy: "random", NodeIds: []int{second.Id, first.Id, second.Id}, SortOrder: 1, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create with nodes only: %v", err)
	}
	stored, err := svc.Get(created.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if want := []int{second.Id, first.Id}; !slices.Equal(stored.NodeIds, want) {
		t.Fatalf("NodeIds = %v, want %v (order kept, repeat dropped)", stored.NodeIds, want)
	}
	if len(stored.InboundIds) != 0 {
		t.Fatalf("InboundIds = %v, want none", stored.InboundIds)
	}

	updated, err := svc.Update(created.Id, &model.SubBalancer{
		Remark: "by-node", Strategy: "random", NodeIds: []int{third.Id}, InboundIds: []int{9}, SortOrder: 1,
	}, nil)
	if err != nil {
		t.Fatalf("update nodes: %v", err)
	}
	if want := []int{third.Id}; !slices.Equal(updated.NodeIds, want) {
		t.Fatalf("updated NodeIds = %v, want %v", updated.NodeIds, want)
	}
	reread, err := svc.Get(created.Id)
	if err != nil {
		t.Fatalf("get after update: %v", err)
	}
	if !slices.Equal(reread.NodeIds, []int{third.Id}) || !slices.Equal(reread.InboundIds, []int{9}) {
		t.Fatalf("after update NodeIds=%v InboundIds=%v, want [%d] and [9]", reread.NodeIds, reread.InboundIds, third.Id)
	}

	// An update that leaves neither an inbound nor a node would leave the
	// balancer without members, so it is refused and the stored row is kept.
	if _, err := svc.Update(created.Id, &model.SubBalancer{Remark: "by-node", Strategy: "random", SortOrder: 1}, nil); err == nil {
		t.Fatal("update to no inbound and no node must fail")
	}
	kept, err := svc.Get(created.Id)
	if err != nil {
		t.Fatalf("get after refused update: %v", err)
	}
	if !slices.Equal(kept.NodeIds, []int{third.Id}) {
		t.Fatalf("refused update changed NodeIds to %v", kept.NodeIds)
	}
}

func TestSubBalancerServiceNodeValidation(t *testing.T) {
	setupSubBalancerDB(t)
	svc := &SubBalancerService{}
	known := seedBalancerNode(t, "known")

	cases := []struct {
		name string
		row  model.SubBalancer
		want string
	}{
		{"neither inbound nor node", model.SubBalancer{Remark: "x", Strategy: "random"}, "at least one inbound or node"},
		{"unknown node", model.SubBalancer{Remark: "x", Strategy: "random", NodeIds: []int{known.Id + 100}}, "does not exist"},
		{"known beside unknown node", model.SubBalancer{Remark: "x", Strategy: "random", NodeIds: []int{known.Id, known.Id + 100}}, "does not exist"},
		{"unknown node beside an inbound", model.SubBalancer{Remark: "x", Strategy: "random", InboundIds: []int{1}, NodeIds: []int{known.Id + 100}}, "does not exist"},
		{"zero node", model.SubBalancer{Remark: "x", Strategy: "random", NodeIds: []int{0}}, "must be positive"},
		{"negative node", model.SubBalancer{Remark: "x", Strategy: "random", NodeIds: []int{known.Id, -3}}, "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(&tc.row)
			if err == nil {
				t.Fatal("create must fail")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
	var rows int64
	if err := database.GetDB().Model(&model.SubBalancer{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Fatalf("%d balancers stored after refused creates, want 0", rows)
	}
}

// Deleting a node takes it out of every balancer that selects it and leaves the
// other members, and the other balancers, as they were.
func TestNodeDeleteDropsItFromSubBalancers(t *testing.T) {
	setupSubBalancerDB(t)
	svc := &SubBalancerService{}
	gone := seedBalancerNode(t, "gone")
	kept := seedBalancerNode(t, "kept")

	both, err := svc.Create(&model.SubBalancer{Remark: "both", Strategy: "random", InboundIds: []int{5}, NodeIds: []int{gone.Id, kept.Id}, SortOrder: 1, Enabled: true})
	if err != nil {
		t.Fatalf("create both: %v", err)
	}
	only, err := svc.Create(&model.SubBalancer{Remark: "only", Strategy: "random", NodeIds: []int{gone.Id}, SortOrder: 2, Enabled: true})
	if err != nil {
		t.Fatalf("create only: %v", err)
	}
	other, err := svc.Create(&model.SubBalancer{Remark: "other", Strategy: "random", NodeIds: []int{kept.Id}, SortOrder: 3, Enabled: true})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	if err := (&NodeService{}).Delete(gone.Id); err != nil {
		t.Fatalf("delete node: %v", err)
	}

	check := func(id int, wantNodes, wantInbounds []int) {
		t.Helper()
		got, err := svc.Get(id)
		if err != nil {
			t.Fatalf("get %d: %v", id, err)
		}
		if len(got.NodeIds) != len(wantNodes) || (len(wantNodes) > 0 && !slices.Equal(got.NodeIds, wantNodes)) {
			t.Fatalf("balancer %d NodeIds = %v, want %v", id, got.NodeIds, wantNodes)
		}
		if len(got.InboundIds) != len(wantInbounds) || (len(wantInbounds) > 0 && !slices.Equal(got.InboundIds, wantInbounds)) {
			t.Fatalf("balancer %d InboundIds = %v, want %v", id, got.InboundIds, wantInbounds)
		}
	}
	check(both.Id, []int{kept.Id}, []int{5})
	check(only.Id, nil, nil)
	check(other.Id, []int{kept.Id}, nil)
}

// A node that cannot be deleted because it still owns inbounds stays in the
// balancers that select it.
func TestNodeDeleteRefusedKeepsSubBalancerNodes(t *testing.T) {
	setupSubBalancerDB(t)
	svc := &SubBalancerService{}
	node := seedBalancerNode(t, "busy")
	nodeID := node.Id
	if err := database.GetDB().Create(&model.Inbound{
		UserId: 1, Tag: "busy-in", Enable: true, Listen: "203.0.113.7", Port: 5101, Protocol: model.VLESS,
		Remark: "busy-in", Settings: `{}`, StreamSettings: `{}`, NodeID: &nodeID,
	}).Error; err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	bal, err := svc.Create(&model.SubBalancer{Remark: "bal", Strategy: "random", NodeIds: []int{node.Id}, SortOrder: 1, Enabled: true})
	if err != nil {
		t.Fatalf("create balancer: %v", err)
	}

	if err := (&NodeService{}).Delete(node.Id); err == nil {
		t.Fatal("delete must be refused while an inbound is attached")
	}
	got, err := svc.Get(bal.Id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !slices.Equal(got.NodeIds, []int{node.Id}) {
		t.Fatalf("NodeIds = %v after a refused delete, want [%d]", got.NodeIds, node.Id)
	}
}
