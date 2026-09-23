package service

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Mirrors TestMtprotoRoutesThroughXray/TestNormalizeMtprotoXrayPort in
// inbound_mtproto_test.go almost exactly -- tproxyRoutesThroughXray/
// normalizeTproxyXrayPort share the identical contract with their mtproto
// counterparts, differing only in which protocol/package they gate on.

func TestTproxyRoutesThroughXray(t *testing.T) {
	cases := map[string]struct {
		ib   *model.Inbound
		want bool
	}{
		"routed":     {&model.Inbound{Protocol: model.Tproxy, Settings: `{"routeThroughXray":true}`}, true},
		"off":        {&model.Inbound{Protocol: model.Tproxy, Settings: `{"routeThroughXray":false}`}, false},
		"absent":     {&model.Inbound{Protocol: model.Tproxy, Settings: `{}`}, false},
		"non-tproxy": {&model.Inbound{Protocol: model.VLESS, Settings: `{"routeThroughXray":true}`}, false},
		"bad json":   {&model.Inbound{Protocol: model.Tproxy, Settings: `{nope`}, false},
		"nil":        {nil, false},
	}
	for name, c := range cases {
		if got := tproxyRoutesThroughXray(c.ib); got != c.want {
			t.Fatalf("%s: got %v want %v", name, got, c.want)
		}
	}
}

func TestNormalizeTproxyXrayPort(t *testing.T) {
	s := &InboundService{}

	// Non-tproxy inbounds are left alone.
	ib := &model.Inbound{Protocol: model.VLESS, Settings: `{"x":1}`}
	if err := s.normalizeTproxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if ib.Settings != `{"x":1}` {
		t.Fatalf("non-tproxy settings must be untouched, got %s", ib.Settings)
	}

	// Routing on with no existing port allocates a fresh one.
	ib = &model.Inbound{Protocol: model.Tproxy, Settings: `{"routeThroughXray":true}`}
	if err := s.normalizeTproxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p <= 0 {
		t.Fatalf("a routed inbound must get a port, got %d", p)
	}

	// On update, the stored port wins over both a missing and a client-echoed
	// value — the backend owns it, so no churn and no client override.
	ib = &model.Inbound{Protocol: model.Tproxy, Settings: `{"routeThroughXray":true,"routeXrayPort":99999}`}
	if err := s.normalizeTproxyXrayPort(ib, `{"routeThroughXray":true,"routeXrayPort":51100}`); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 51100 {
		t.Fatalf("stored port must win, got %d", p)
	}

	// An already-present port (no old settings) is stable and not re-marshaled.
	const stable = `{"routeThroughXray":true,"routeXrayPort":52100}`
	ib = &model.Inbound{Protocol: model.Tproxy, Settings: stable}
	if err := s.normalizeTproxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if ib.Settings != stable {
		t.Fatalf("stable settings must pass through untouched, got %s", ib.Settings)
	}

	// Turning routing off strips both the bridge port and the inert outbound.
	ib = &model.Inbound{Protocol: model.Tproxy, Settings: `{"routeThroughXray":false,"routeXrayPort":53100,"outboundTag":"warp"}`}
	if err := s.normalizeTproxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 0 {
		t.Fatalf("disabling routing must drop the port, got %d", p)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["outboundTag"]; ok {
		t.Fatalf("disabling routing must drop the inert outbound tag, got %s", ib.Settings)
	}
}
