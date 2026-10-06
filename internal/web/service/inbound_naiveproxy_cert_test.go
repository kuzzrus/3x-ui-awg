package service

import (
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestNaiveProxyCertTargets(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}

	seedInboundConflict(t, "np-auto", "", 46401, model.NaiveProxy, "",
		`{"domain":"auto.example.com","certMode":"auto","acmeEmail":"ops@example.com","clients":[]}`)
	seedInboundConflict(t, "np-manual", "", 46402, model.NaiveProxy, "",
		`{"domain":"manual.example.com","certFile":"/etc/np.crt","keyFile":"/etc/np.key","clients":[]}`)
	if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", "np-manual").Update("enable", false).Error; err != nil {
		t.Fatalf("switch inbound off: %v", err)
	}
	nodeID := 5
	seedInboundConflictNode(t, "np-node", "", 46403, model.NaiveProxy, "",
		`{"domain":"node.example.com","certMode":"auto","clients":[]}`, &nodeID)
	seedInboundConflict(t, "vless-other", "", 46404, model.VLESS, "", `{"clients":[]}`)

	targets, err := svc.NaiveProxyCertTargets()
	if err != nil {
		t.Fatalf("NaiveProxyCertTargets: %v", err)
	}

	want := []NaiveProxyCertTarget{
		{Enable: true, CertSettings: naiveproxy.CertSettings{Domain: "auto.example.com", Mode: naiveproxy.CertAuto, Email: "ops@example.com"}},
		{Enable: false, CertSettings: naiveproxy.CertSettings{Domain: "manual.example.com", Mode: naiveproxy.CertManual, CertFile: "/etc/np.crt"}},
	}
	if len(targets) != len(want) {
		t.Fatalf("got %d targets, want the two local NaiveProxy inbounds (not the node-owned or VLESS ones): %+v", len(targets), targets)
	}
	for i, w := range want {
		got := targets[i]
		if got.InboundId == 0 || got.Enable != w.Enable || got.CertSettings != w.CertSettings {
			t.Errorf("target %d = %+v, want %+v", i, got, w)
		}
	}
	if targets[0].InboundId >= targets[1].InboundId {
		t.Errorf("targets are not in inbound order: %d then %d", targets[0].InboundId, targets[1].InboundId)
	}
}

func TestDesiredNaiveProxyInstancesCarryTheCertMode(t *testing.T) {
	setupConflictDB(t)
	svc := &InboundService{}
	seedInboundConflict(t, "np-auto-desired", "", 46405, model.NaiveProxy, "",
		`{"domain":"auto.example.com","certMode":"auto","clients":[{"email":"alice","naiveProxyPassword":"pw-a","enable":true}]}`)
	seeded := loadInboundByTag(t, "np-auto-desired")
	seedClientTraffic(t, seeded.Id, "alice", true)

	instances, err := svc.DesiredNaiveProxyInstances()
	if err != nil {
		t.Fatalf("DesiredNaiveProxyInstances: %v", err)
	}
	if len(instances) != 1 || instances[0].CertMode != naiveproxy.CertAuto {
		t.Fatalf("instances = %+v, want one automatic-certificate instance", instances)
	}
}

func TestValidateNaiveProxyCert(t *testing.T) {
	inbound := func(protocol model.Protocol, settings string) *model.Inbound {
		return &model.Inbound{Protocol: protocol, Settings: settings}
	}
	for _, tc := range []struct {
		name    string
		in      *model.Inbound
		wantErr string // "" means accepted
	}{
		{"a valid automatic inbound", inbound(model.NaiveProxy, `{"domain":"naive.example.com","certMode":"auto"}`), ""},
		{"with an email of its own", inbound(model.NaiveProxy, `{"domain":"naive.example.com","certMode":"auto","acmeEmail":"ops@example.com"}`), ""},
		{"manual files need no domain check", inbound(model.NaiveProxy, `{"domain":"203.0.113.7","certFile":"/c.pem","keyFile":"/k.pem"}`), ""},
		{"another protocol is none of its business", inbound(model.VLESS, `{"certMode":"auto"}`), ""},
		{"an IP address", inbound(model.NaiveProxy, `{"domain":"203.0.113.7","certMode":"auto"}`), "IP address"},
		{"no domain", inbound(model.NaiveProxy, `{"certMode":"auto"}`), "domain is required"},
		{"a wildcard", inbound(model.NaiveProxy, `{"domain":"*.example.com","certMode":"auto"}`), "wildcard"},
		{"a URL instead of a domain", inbound(model.NaiveProxy, `{"domain":"https://naive.example.com","certMode":"auto"}`), "not a valid domain"},
		{"a contact that is not an address", inbound(model.NaiveProxy, `{"domain":"naive.example.com","certMode":"auto","acmeEmail":"ops"}`), "not an email address"},
		{"a contact with a display name", inbound(model.NaiveProxy, `{"domain":"naive.example.com","certMode":"auto","acmeEmail":"Ops <ops@example.com>"}`), "not an email address"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNaiveProxyCert(tc.in)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("validateNaiveProxyCert = %v, want it accepted", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("validateNaiveProxyCert = %v, want an error mentioning %q", err, tc.wantErr)
			}
		})
	}
}

// The form can only be trusted so far: the API takes the same settings, and a domain Let's
// Encrypt can never validate would sit in "obtaining" forever instead of being refused up front.
func TestInboundSaveRefusesAnAutomaticCertificateItCannotOrder(t *testing.T) {
	setupConflictDB(t)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	svc := &InboundService{}

	_, _, err := svc.AddInbound(&model.Inbound{
		Enable:   true,
		Port:     46410,
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"203.0.113.7","certMode":"auto","clients":[]}`,
	})
	if err == nil || !strings.Contains(err.Error(), "IP address") {
		t.Fatalf("AddInbound with an IP as the domain = %v, want a refusal that says so", err)
	}

	created, _, err := svc.AddInbound(&model.Inbound{
		Enable:   true,
		Port:     46411,
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"naive.example.com","certMode":"auto","clients":[]}`,
	})
	if err != nil {
		t.Fatalf("AddInbound with a valid automatic domain: %v", err)
	}
	created.Settings = `{"domain":"naive.example.com","certMode":"auto","acmeEmail":"not-an-address","clients":[]}`
	if _, _, err := svc.UpdateInbound(created); err == nil || !strings.Contains(err.Error(), "not an email address") {
		t.Fatalf("UpdateInbound with a bad contact = %v, want a refusal that says so", err)
	}
}
