package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type agentTrafficFixture struct {
	t      *testing.T
	svc    *InboundService
	nodeID int
	tag    string
}

// newAgentTrafficFixture is an agent node with one inbound and clients a@x and b@x.
func newAgentTrafficFixture(t *testing.T) *agentTrafficFixture {
	t.Helper()
	setupSettingTestDB(t)
	f := &agentTrafficFixture{t: t, svc: &InboundService{}, nodeID: seedAgentRow(t, "agent"), tag: "n1-vless"}
	in := seedInboundOn(t, &f.nodeID, f.tag, model.VLESS, true, []model.Client{
		{Email: "a@x", ID: "11111111-1111-1111-1111-111111111111", Enable: true},
		{Email: "b@x", ID: "22222222-2222-2222-2222-222222222222", Enable: true},
	})
	for _, email := range []string{"a@x", "b@x"} {
		if err := database.GetDB().Create(&xray.ClientTraffic{InboundId: in.Id, Email: email, Enable: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *agentTrafficFixture) poll(startedAt int64, users map[string]agentproto.Counter, inbound agentproto.Counter) {
	f.t.Helper()
	stats := &agentproto.Stats{
		XrayStartedAt: startedAt,
		Users:         users,
		Inbounds:      map[string]agentproto.Counter{f.tag: inbound},
	}
	if err := f.svc.AddAgentTraffic(f.nodeID, stats); err != nil {
		f.t.Fatalf("AddAgentTraffic: %v", err)
	}
}

func (f *agentTrafficFixture) client(email string) (up, down int64) {
	f.t.Helper()
	var ct xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", email).First(&ct).Error; err != nil {
		f.t.Fatalf("client_traffics row of %s: %v", email, err)
	}
	return ct.Up, ct.Down
}

func (f *agentTrafficFixture) inbound() (up, down int64) {
	f.t.Helper()
	var in model.Inbound
	if err := database.GetDB().Where("tag = ?", f.tag).First(&in).Error; err != nil {
		f.t.Fatal(err)
	}
	return in.Up, in.Down
}

func (f *agentTrafficFixture) wantClient(email string, up, down int64) {
	f.t.Helper()
	if gotUp, gotDown := f.client(email); gotUp != up || gotDown != down {
		f.t.Fatalf("%s has %d up %d down, want %d up %d down", email, gotUp, gotDown, up, down)
	}
}

func (f *agentTrafficFixture) wantInbound(up, down int64) {
	f.t.Helper()
	if gotUp, gotDown := f.inbound(); gotUp != up || gotDown != down {
		f.t.Fatalf("inbound has %d up %d down, want %d up %d down", gotUp, gotDown, up, down)
	}
}

func counters(pairs ...int64) map[string]agentproto.Counter {
	out := map[string]agentproto.Counter{}
	for i, email := range []string{"a@x", "b@x"} {
		if 2*i+1 < len(pairs) {
			out[email] = agentproto.Counter{Up: pairs[2*i], Down: pairs[2*i+1]}
		}
	}
	return out
}

func TestAddAgentTrafficCountsTheFirstPollWhole(t *testing.T) {
	f := newAgentTrafficFixture(t)

	f.poll(1000, counters(10, 20), agentproto.Counter{Up: 30, Down: 40})

	f.wantClient("a@x", 10, 20)
	f.wantClient("b@x", 0, 0)
	f.wantInbound(30, 40)
	var node model.Node
	if err := database.GetDB().First(&node, f.nodeID).Error; err != nil || node.AgentStartedAt != 1000 {
		t.Fatalf("node remembers core start %d (%v), want 1000", node.AgentStartedAt, err)
	}
}

func TestAddAgentTrafficAddsOnlyTheGrowthOfTheSameCore(t *testing.T) {
	f := newAgentTrafficFixture(t)

	f.poll(1000, counters(10, 20), agentproto.Counter{Up: 30, Down: 40})
	f.poll(1000, counters(15, 26), agentproto.Counter{Up: 36, Down: 41})
	f.poll(1000, counters(15, 26), agentproto.Counter{Up: 36, Down: 41})

	f.wantClient("a@x", 15, 26)
	f.wantInbound(36, 41)
}

func TestAddAgentTrafficCountsACoreThatBeganAgainWhole(t *testing.T) {
	f := newAgentTrafficFixture(t)

	f.poll(1000, counters(100, 200, 50, 60), agentproto.Counter{Up: 300, Down: 400})
	f.poll(2000, counters(7, 9), agentproto.Counter{Up: 11, Down: 12})
	f.wantClient("a@x", 107, 209)
	f.wantClient("b@x", 50, 60)
	f.wantInbound(311, 412)

	f.poll(2000, counters(8, 9), agentproto.Counter{Up: 12, Down: 12})
	f.wantClient("a@x", 108, 209)

	// b@x was not met by the new core when it began, so what it counted earlier must not
	// be taken off its first counter now.
	f.poll(2000, counters(8, 9, 3, 3), agentproto.Counter{Up: 12, Down: 12})
	f.wantClient("b@x", 53, 63)
}

func TestAddAgentTrafficTakesACounterBelowItsBaselineForAReset(t *testing.T) {
	f := newAgentTrafficFixture(t)

	f.poll(1000, counters(100, 200), agentproto.Counter{Up: 300, Down: 400})
	f.poll(1000, counters(4, 250), agentproto.Counter{Up: 310, Down: 390})

	f.wantClient("a@x", 104, 250)
	f.wantInbound(310, 790)
}

func TestAddAgentTrafficIsNotUndoneByAResetOnTheMaster(t *testing.T) {
	f := newAgentTrafficFixture(t)
	f.poll(1000, counters(10, 20), agentproto.Counter{Up: 30, Down: 40})

	// What resetting a client's usage does: zero its row and drop the node baselines.
	db := database.GetDB()
	if err := db.Model(&xray.ClientTraffic{}).Where("email = ?", "a@x").Updates(map[string]any{"up": 0, "down": 0}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("1 = 1").Delete(&model.NodeClientTraffic{}).Error; err != nil {
		t.Fatal(err)
	}

	f.poll(1000, counters(12, 25), agentproto.Counter{Up: 30, Down: 40})
	f.wantClient("a@x", 2, 5)
}

func TestAddAgentTrafficLeavesWhatIsNotTheMastersAlone(t *testing.T) {
	f := newAgentTrafficFixture(t)

	stats := &agentproto.Stats{
		XrayStartedAt: 1000,
		Users:         map[string]agentproto.Counter{"stranger@x": {Up: 5, Down: 5}, "a@x": {Up: 1, Down: 2}},
		Inbounds:      map[string]agentproto.Counter{"api": {Up: 9, Down: 9}, f.tag: {Up: 3, Down: 4}},
	}
	if err := f.svc.AddAgentTraffic(f.nodeID, stats); err != nil {
		t.Fatalf("AddAgentTraffic: %v", err)
	}
	f.wantClient("a@x", 1, 2)
	f.wantInbound(3, 4)
}

func TestAddAgentTrafficIgnoresACoreThatIsNotRunning(t *testing.T) {
	f := newAgentTrafficFixture(t)
	f.poll(1000, counters(10, 20), agentproto.Counter{Up: 30, Down: 40})

	stopped := &agentproto.Stats{Users: map[string]agentproto.Counter{}, Inbounds: map[string]agentproto.Counter{}}
	if err := f.svc.AddAgentTraffic(f.nodeID, stopped); err != nil {
		t.Fatalf("AddAgentTraffic: %v", err)
	}
	f.poll(1000, counters(10, 20), agentproto.Counter{Up: 30, Down: 40})

	f.wantClient("a@x", 10, 20)
	f.wantInbound(30, 40)
}

// Depletion is the master's: a client that passed its quota leaves the next rendered config.
func TestAddAgentTrafficDepletesAClientThatPassedItsQuota(t *testing.T) {
	f := newAgentTrafficFixture(t)
	if err := database.GetDB().Model(&xray.ClientTraffic{}).Where("email = ?", "a@x").Update("total", 100).Error; err != nil {
		t.Fatal(err)
	}

	f.poll(1000, counters(80, 70), agentproto.Counter{Up: 80, Down: 70})
	if _, _, err := f.svc.AddTraffic(nil, nil); err != nil {
		t.Fatalf("AddTraffic: %v", err)
	}

	var ct xray.ClientTraffic
	if err := database.GetDB().Where("email = ?", "a@x").First(&ct).Error; err != nil || ct.Enable {
		t.Fatalf("a@x = %+v (%v), want it switched off after 150 of 100 bytes", ct, err)
	}
	var node model.Node
	if err := database.GetDB().First(&node, f.nodeID).Error; err != nil || !node.ConfigDirty {
		t.Fatalf("node dirty = %v (%v), want it marked so the next push drops the client", node.ConfigDirty, err)
	}
	cfg, err := (&XrayService{}).GetAgentConfig(f.nodeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.InboundConfigs {
		if in.Tag == f.tag && strings.Contains(string(in.Settings), `"a@x"`) {
			t.Fatalf("the rendered config still carries the depleted client: %s", in.Settings)
		}
	}
}

func TestDeletingAnAgentNodeDropsWhatWasAccountedOfIt(t *testing.T) {
	setupSettingTestDB(t)
	nodeID := seedAgentRow(t, "agent")
	svc := &InboundService{}
	stats := &agentproto.Stats{XrayStartedAt: 1000, Users: map[string]agentproto.Counter{"a@x": {Up: 1, Down: 1}}}
	if err := svc.AddAgentTraffic(nodeID, stats); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := database.GetDB().Model(&model.AgentCounter{}).Where("node_id = ?", nodeID).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("%d counters stored (%v), want 1", count, err)
	}

	if err := (&NodeService{}).Delete(nodeID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := database.GetDB().Model(&model.AgentCounter{}).Where("node_id = ?", nodeID).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("%d counters left after the node was deleted (%v)", count, err)
	}
}
