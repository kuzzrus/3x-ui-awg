package service

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const naiveDesiredSettings = `{"domain":"np.example.com","certFile":"/c.pem","keyFile":"/k.pem","clients":[` +
	`{"email":"alice","naiveProxyPassword":"pw-a","enable":true},` +
	`{"email":"bob","naiveProxyPassword":"pw-b","enable":true},` +
	`{"email":"carol","naiveProxyPassword":"pw-c","enable":false}]}`

func TestDesiredNaiveProxyInstancesFiltersDepleted(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	seedInboundConflict(t, "np-desired", "", 46101, model.NaiveProxy, "", naiveDesiredSettings)
	served := loadInboundByTag(t, "np-desired")
	seedClientTraffic(t, served.Id, "alice", true)
	seedClientTraffic(t, served.Id, "bob", false)
	seedClientTraffic(t, served.Id, "carol", true)

	seedInboundConflict(t, "np-all-depleted", "", 46102, model.NaiveProxy, "",
		`{"domain":"d.example.com","certFile":"/c.pem","keyFile":"/k.pem","clients":[{"email":"dave","naiveProxyPassword":"pw-d","enable":true}]}`)
	depleted := loadInboundByTag(t, "np-all-depleted")
	seedClientTraffic(t, depleted.Id, "dave", false)

	seedInboundConflict(t, "np-switched-off", "", 46104, model.NaiveProxy, "",
		`{"domain":"o.example.com","certFile":"/c.pem","keyFile":"/k.pem","clients":[{"email":"frank","naiveProxyPassword":"pw-f","enable":true}]}`)
	if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", "np-switched-off").Update("enable", false).Error; err != nil {
		t.Fatalf("switch inbound off: %v", err)
	}

	nodeID := 5
	seedInboundConflictNode(t, "np-node-owned", "", 46103, model.NaiveProxy, "",
		`{"domain":"n.example.com","certFile":"/c.pem","keyFile":"/k.pem","clients":[{"email":"erin","naiveProxyPassword":"pw-e","enable":true}]}`,
		&nodeID)

	instances, err := svc.DesiredNaiveProxyInstances()
	if err != nil {
		t.Fatalf("DesiredNaiveProxyInstances: %v", err)
	}

	t.Run("depletedAndDisabledClientsExcluded", func(t *testing.T) {
		if len(instances) != 1 {
			t.Fatalf("expected exactly the served inbound (not the all-depleted, switched-off or node-owned ones), got %d: %+v", len(instances), instances)
		}
		if instances[0].Id != served.Id || instances[0].Tag != "np-desired" {
			t.Fatalf("expected inbound %d tagged np-desired, got %d tagged %q", served.Id, instances[0].Id, instances[0].Tag)
		}
		want := []naiveproxy.Client{{Email: "alice", Username: "alice", Password: "pw-a"}}
		if !reflect.DeepEqual(instances[0].Clients, want) {
			t.Fatalf("served clients: got %+v, want %+v", instances[0].Clients, want)
		}
	})

	t.Run("matchesInteractivePushFiltering", func(t *testing.T) {
		built, err := svc.buildInboundForLocalRuntime(database.GetDB(), served)
		if err != nil {
			t.Fatalf("buildInboundForLocalRuntime: %v", err)
		}
		pushInst, ok := naiveproxy.InstanceFromInbound(built)
		if !ok {
			t.Fatal("push path must produce an instance")
		}
		if !reflect.DeepEqual(pushInst.Clients, instances[0].Clients) {
			t.Fatalf("push and job client sets diverge: push %+v, job %+v", pushInst.Clients, instances[0].Clients)
		}
	})
}

// The accounting job meters traffic into AddTraffic. A quota it records but that
// nothing enforces would be worthless: once a client is used up it must leave the served set.
func TestNaiveProxyMeteredTrafficDepletesAClientOutOfTheServedSet(t *testing.T) {
	setupConflictDB(t)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	svc := &InboundService{}
	db := database.GetDB()

	seedInboundConflict(t, "np-quota", "", 46111, model.NaiveProxy, "",
		`{"domain":"np.example.com","certFile":"/c.pem","keyFile":"/k.pem","clients":[`+
			`{"email":"alice","naiveProxyPassword":"pw-a","enable":true},`+
			`{"email":"bob","naiveProxyPassword":"pw-b","enable":true}]}`)
	ib := loadInboundByTag(t, "np-quota")
	for email, quota := range map[string]int64{"alice": 300000, "bob": 0} {
		rec := &model.ClientRecord{Email: email, SubID: "sub-" + email, Enable: true}
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("seed client %s: %v", email, err)
		}
		if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: ib.Id}).Error; err != nil {
			t.Fatalf("attach client %s: %v", email, err)
		}
		if err := db.Create(&xray.ClientTraffic{InboundId: ib.Id, Email: email, Enable: true, Total: quota}).Error; err != nil {
			t.Fatalf("seed traffic %s: %v", email, err)
		}
	}

	before, err := svc.DesiredNaiveProxyInstances()
	if err != nil || len(before) != 1 || len(before[0].Clients) != 2 {
		t.Fatalf("before any traffic both clients must be served: %+v, err %v", before, err)
	}

	if _, _, err := svc.AddTraffic(nil, []*xray.ClientTraffic{
		{Email: "alice", Up: 100000, Down: 250000},
		{Email: "bob", Up: 10, Down: 20},
	}); err != nil {
		t.Fatalf("AddTraffic: %v", err)
	}

	traffic := func(email string) xray.ClientTraffic {
		t.Helper()
		var row xray.ClientTraffic
		if err := db.Where("email = ?", email).First(&row).Error; err != nil {
			t.Fatalf("load traffic %s: %v", email, err)
		}
		return row
	}
	if a := traffic("alice"); a.Up != 100000 || a.Down != 250000 || a.Enable {
		t.Fatalf("alice went over her 300000 quota, want her bytes recorded and her disabled: %+v", a)
	}
	if b := traffic("bob"); b.Up != 10 || b.Down != 20 || !b.Enable {
		t.Fatalf("bob is unlimited, want his bytes recorded and him still enabled: %+v", b)
	}

	after, err := svc.DesiredNaiveProxyInstances()
	if err != nil {
		t.Fatalf("DesiredNaiveProxyInstances: %v", err)
	}
	if len(after) != 1 || len(after[0].Clients) != 1 || after[0].Clients[0].Email != "bob" {
		t.Fatalf("after alice ran out only bob may be served, got %+v", after)
	}

	var stored struct {
		Clients []struct {
			Email  string `json:"email"`
			Enable bool   `json:"enable"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(loadInboundByTag(t, "np-quota").Settings), &stored); err != nil {
		t.Fatal(err)
	}
	for _, c := range stored.Clients {
		if c.Email == "alice" && c.Enable {
			t.Fatal("alice must be marked disabled in the inbound's stored settings too, or a restart would serve her again")
		}
	}
}
