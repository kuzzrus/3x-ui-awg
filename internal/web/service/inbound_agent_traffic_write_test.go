package service

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// failClientWrites makes the update of the email's client_traffics row fail until heal is called
// or the test ends.
func failClientWrites(t *testing.T, email string) (heal func()) {
	t.Helper()
	db := database.GetDB()
	const name = "test:fail_client_write"
	var mu sync.Mutex
	failing := true
	if err := db.Callback().Raw().Before("gorm:raw").Register(name, func(tx *gorm.DB) {
		mu.Lock()
		defer mu.Unlock()
		if !failing || !strings.HasPrefix(tx.Statement.SQL.String(), "UPDATE client_traffics SET up") {
			return
		}
		for _, v := range tx.Statement.Vars {
			if v == email {
				tx.AddError(errors.New("injected write failure"))
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Raw().Remove(name) })
	return func() {
		mu.Lock()
		failing = false
		mu.Unlock()
	}
}

// mostStatementVars records how many variables the widest statement binds until the test ends.
func mostStatementVars(t *testing.T) func() int {
	t.Helper()
	db := database.GetDB()
	const name = "test:most_statement_vars"
	var mu sync.Mutex
	most := 0
	record := func(tx *gorm.DB) {
		mu.Lock()
		most = max(most, len(tx.Statement.Vars))
		mu.Unlock()
	}
	for _, register := range []func() error{
		func() error { return db.Callback().Query().After("gorm:query").Register(name, record) },
		func() error { return db.Callback().Create().After("gorm:create").Register(name, record) },
		func() error { return db.Callback().Raw().After("gorm:raw").Register(name, record) },
	} {
		if err := register(); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(name)
		_ = db.Callback().Create().Remove(name)
		_ = db.Callback().Raw().Remove(name)
	})
	return func() int {
		mu.Lock()
		defer mu.Unlock()
		return most
	}
}

func TestAddAgentTrafficKeepsEveryStatementUnderTheVariableCeiling(t *testing.T) {
	f := newAgentTrafficFixture(t)
	db := database.GetDB()
	var in model.Inbound
	if err := db.Where("tag = ?", f.tag).First(&in).Error; err != nil {
		t.Fatal(err)
	}
	const users = 2*sqliteMaxVars + 7
	rows := make([]*xray.ClientTraffic, 0, users)
	reported := make(map[string]agentproto.Counter, users)
	for i := range users {
		email := fmt.Sprintf("u%04d@x", i)
		rows = append(rows, &xray.ClientTraffic{InboundId: in.Id, Email: email, Enable: true})
		reported[email] = agentproto.Counter{Up: int64(i + 1), Down: 2}
	}
	if err := db.CreateInBatches(rows, 50).Error; err != nil {
		t.Fatal(err)
	}
	widest := mostStatementVars(t)

	stats := &agentproto.Stats{
		XrayStartedAt: 1000,
		Users:         reported,
		Inbounds:      map[string]agentproto.Counter{f.tag: {Up: 1, Down: 1}},
	}
	if err := f.svc.AddAgentTraffic(f.nodeID, stats); err != nil {
		t.Fatalf("AddAgentTraffic: %v", err)
	}

	if got := widest(); got > sqliteMaxVars {
		t.Fatalf("a statement bound %d variables, want at most %d", got, sqliteMaxVars)
	}
	f.wantClient("u0000@x", 1, 2)
	f.wantClient(fmt.Sprintf("u%04d@x", users-1), users, 2)
}

func TestAddAgentTrafficLeavesTheBaselinesAloneWhenAClientRowCannotBeWritten(t *testing.T) {
	f := newAgentTrafficFixture(t)
	heal := failClientWrites(t, "a@x")
	stats := &agentproto.Stats{
		XrayStartedAt: 1000,
		Users:         counters(10, 20),
		Inbounds:      map[string]agentproto.Counter{f.tag: {Up: 30, Down: 40}},
	}

	if err := f.svc.AddAgentTraffic(f.nodeID, stats); err == nil {
		t.Fatal("AddAgentTraffic reported success although a client row could not be written")
	}
	var stored int64
	if err := database.GetDB().Model(&model.AgentCounter{}).Where("node_id = ?", f.nodeID).Count(&stored).Error; err != nil {
		t.Fatal(err)
	}
	var node model.Node
	if err := database.GetDB().First(&node, f.nodeID).Error; err != nil {
		t.Fatal(err)
	}
	if stored != 0 || node.AgentStartedAt != 0 {
		t.Fatalf("%d baselines stored and core start %d, want none: what failed would never be added", stored, node.AgentStartedAt)
	}

	heal()
	if err := f.svc.AddAgentTraffic(f.nodeID, stats); err != nil {
		t.Fatalf("AddAgentTraffic after the write works: %v", err)
	}
	f.wantClient("a@x", 10, 20)
	f.wantInbound(30, 40)
}

// Xray has reset the counters of the panel's own core by the time they are added, so there one
// row that cannot be written must not take the others down with it.
func TestAddClientTrafficKeepsGoingPastARowItCannotWrite(t *testing.T) {
	f := newAgentTrafficFixture(t)
	failClientWrites(t, "a@x")

	err := f.svc.addClientTraffic(database.GetDB(), []*xray.ClientTraffic{
		{Email: "a@x", Up: 5, Down: 6},
		{Email: "b@x", Up: 7, Down: 8},
	})
	if err != nil {
		t.Fatalf("addClientTraffic: %v", err)
	}
	f.wantClient("a@x", 0, 0)
	f.wantClient("b@x", 7, 8)
}
