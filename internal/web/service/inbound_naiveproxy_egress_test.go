package service

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

func TestNaiveProxyRoutesThroughXray(t *testing.T) {
	cases := map[string]struct {
		ib   *model.Inbound
		want bool
	}{
		"routed":         {&model.Inbound{Protocol: model.NaiveProxy, Settings: `{"routeThroughXray":true}`}, true},
		"off":            {&model.Inbound{Protocol: model.NaiveProxy, Settings: `{"routeThroughXray":false}`}, false},
		"absent":         {&model.Inbound{Protocol: model.NaiveProxy, Settings: `{}`}, false},
		"non-naiveproxy": {&model.Inbound{Protocol: model.VLESS, Settings: `{"routeThroughXray":true}`}, false},
		"bad json":       {&model.Inbound{Protocol: model.NaiveProxy, Settings: `{nope`}, false},
		"nil":            {nil, false},
	}
	for name, c := range cases {
		if got := naiveProxyRoutesThroughXray(c.ib); got != c.want {
			t.Fatalf("%s: got %v want %v", name, got, c.want)
		}
	}
}

// Every sidecar whose bridge lives in the generated config must count, or adding,
// dropping or toggling it would leave Xray without (or with a stale) bridge.
func TestSidecarRoutesThroughXray(t *testing.T) {
	for _, protocol := range []model.Protocol{model.MTProto, model.Tproxy, model.NaiveProxy} {
		routed := &model.Inbound{Protocol: protocol, Settings: `{"routeThroughXray":true}`}
		if !sidecarRoutesThroughXray(routed) {
			t.Fatalf("%s: a routed inbound must request a config regen", protocol)
		}
		off := &model.Inbound{Protocol: protocol, Settings: `{"routeThroughXray":false}`}
		if sidecarRoutesThroughXray(off) {
			t.Fatalf("%s: an unrouted inbound must not", protocol)
		}
	}
	if sidecarRoutesThroughXray(&model.Inbound{Protocol: model.VLESS, Settings: `{"routeThroughXray":true}`}) {
		t.Fatal("a non-sidecar protocol never has a bridge to regenerate")
	}
}

func TestNormalizeNaiveProxyXrayPort(t *testing.T) {
	s := &InboundService{}

	// Non-naiveproxy inbounds are left alone.
	ib := &model.Inbound{Protocol: model.VLESS, Settings: `{"x":1}`}
	if err := s.normalizeNaiveProxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if ib.Settings != `{"x":1}` {
		t.Fatalf("non-naiveproxy settings must be untouched, got %s", ib.Settings)
	}

	// Routing on with no existing port allocates a fresh one.
	ib = &model.Inbound{Protocol: model.NaiveProxy, Settings: `{"routeThroughXray":true}`}
	if err := s.normalizeNaiveProxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p <= 0 {
		t.Fatalf("a routed inbound must get a port, got %d", p)
	}

	// On update, the stored port wins over both a missing and a client-echoed
	// value: the backend owns it, so no churn and no client override.
	ib = &model.Inbound{Protocol: model.NaiveProxy, Settings: `{"routeThroughXray":true,"routeXrayPort":99999}`}
	if err := s.normalizeNaiveProxyXrayPort(ib, `{"routeThroughXray":true,"routeXrayPort":51100}`); err != nil {
		t.Fatal(err)
	}
	if p := routeXrayPortOf(t, ib.Settings); p != 51100 {
		t.Fatalf("stored port must win, got %d", p)
	}

	// Turning routing off strips both the bridge port and the inert outbound.
	ib = &model.Inbound{Protocol: model.NaiveProxy, Settings: `{"routeThroughXray":false,"routeXrayPort":53100,"outboundTag":"warp"}`}
	if err := s.normalizeNaiveProxyXrayPort(ib, ""); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["routeXrayPort"]; ok {
		t.Fatalf("disabling routing must drop the port, got %s", ib.Settings)
	}
	if _, ok := parsed["outboundTag"]; ok {
		t.Fatalf("disabling routing must drop the inert outbound tag, got %s", ib.Settings)
	}
}

// A port past 65535 would be written verbatim into the generated Xray config and
// stop Xray from starting, so anywhere the backend picks the port it counts as missing.
func TestNormalizeRoutedXrayPortReplacesAnOutOfRangePort(t *testing.T) {
	allocate := func() (int, error) { return 47001, nil }
	routed := func(port string) string { return `{"routeThroughXray":true,"routeXrayPort":` + port + `}` }
	cases := []struct {
		name     string
		settings string
		old      string
		want     int
	}{
		{"sent above the range", routed("65536"), "", 47001},
		{"sent far above the range", routed("99999999999"), "", 47001},
		{"sent negative", routed("-5"), "", 47001},
		{"sent zero", routed("0"), "", 47001},
		{"stored above the range, none sent", `{"routeThroughXray":true}`, routed("70000"), 47001},
		{"stored above the range, valid one sent", routed("52000"), routed("70000"), 52000},
		{"top of the range is kept", routed("65535"), "", 65535},
		{"bottom of the range is kept", routed("1"), "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ib := &model.Inbound{Protocol: model.NaiveProxy, Settings: c.settings}
			if err := normalizeRoutedXrayPort(ib, c.old, "naiveproxy", allocate); err != nil {
				t.Fatal(err)
			}
			if got := routeXrayPortOf(t, ib.Settings); got != c.want {
				t.Fatalf("port = %d, want %d (settings %s)", got, c.want, ib.Settings)
			}
		})
	}
}

// An out-of-range port already stored (saved before the normalizer checked it)
// must not reach the generated config, or Xray would refuse to start at all.
func TestInjectEgressBridgeSkipsAnOutOfRangePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 99999} {
		cfg := egressTestConfig()
		injectNaiveProxyEgress(cfg, &model.Inbound{
			Tag: "inbound-18443", Protocol: model.NaiveProxy, Enable: true,
			Settings: fmt.Sprintf(`{"routeThroughXray":true,"routeXrayPort":%d}`, port),
		})
		if len(cfg.InboundConfigs) != 1 {
			t.Fatalf("port %d: a bridge with an unusable port must not be injected, got %+v", port, cfg.InboundConfigs)
		}
	}

	cfg := egressTestConfig()
	injectNaiveProxyEgress(cfg, &model.Inbound{
		Tag: "inbound-18443", Protocol: model.NaiveProxy, Enable: true,
		Settings: `{"routeThroughXray":true,"routeXrayPort":65535}`,
	})
	if len(cfg.InboundConfigs) != 2 || cfg.InboundConfigs[1].Port != 65535 {
		t.Fatalf("the top of the range is a valid bridge port, got %+v", cfg.InboundConfigs)
	}
}

func TestInjectNaiveProxyEgress(t *testing.T) {
	cfg := egressTestConfig()
	injectNaiveProxyEgress(cfg, &model.Inbound{
		Tag: "inbound-18443", Protocol: model.NaiveProxy, Enable: true,
		Settings: `{"routeThroughXray":true,"routeXrayPort":50100,"outboundTag":"warp"}`,
	})

	if len(cfg.InboundConfigs) != 2 {
		t.Fatalf("expected the bridge inbound to be appended, got %d", len(cfg.InboundConfigs))
	}
	ib := cfg.InboundConfigs[1]
	if ib.Tag != "inbound-18443" || ib.Protocol != "socks" || ib.Port != 50100 {
		t.Fatalf("unexpected bridge inbound: %+v", ib)
	}
	if string(ib.Listen) != `"127.0.0.1"` {
		t.Fatalf("bridge must listen on loopback, got %s", ib.Listen)
	}
	var routing egressRouting
	if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
		t.Fatal(err)
	}
	first := routing.Rules[0]
	if first.OutboundTag != "warp" || len(first.InboundTag) != 1 || first.InboundTag[0] != "inbound-18443" {
		t.Fatalf("egress rule must bind the inbound tag to the outbound, got %+v", first)
	}
}

func TestInjectNaiveProxyEgress_NotRoutedIsANoOp(t *testing.T) {
	cfg := egressTestConfig()
	before := string(cfg.RouterConfig)
	injectNaiveProxyEgress(cfg, &model.Inbound{
		Tag: "inbound-18443", Protocol: model.NaiveProxy, Enable: true,
		Settings: `{"routeThroughXray":false,"routeXrayPort":50100}`,
	})
	if len(cfg.InboundConfigs) != 1 || string(cfg.RouterConfig) != before {
		t.Fatalf("an unrouted inbound must not touch the config, got %+v", cfg.InboundConfigs)
	}
}

// A routed sidecar inbound is not an Xray inbound itself, so only a config regen
// adds or drops its bridge: every path that creates, toggles or removes it must ask for one.
func TestNaiveProxyBridgeRequestsAConfigRegen(t *testing.T) {
	setupConflictDB(t)
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }}))
	t.Cleanup(func() { runtime.SetManager(nil) })
	svc := &InboundService{}

	const settings = `{"domain":"n.example.com","certFile":"/c.pem","keyFile":"/k.pem","routeThroughXray":%s,"clients":[]}`
	create := func(port int, routed string) (*model.Inbound, bool) {
		t.Helper()
		created, needRestart, err := svc.AddInbound(&model.Inbound{
			Enable:   true,
			Port:     port,
			Protocol: model.NaiveProxy,
			Settings: fmt.Sprintf(settings, routed),
		})
		if err != nil {
			t.Fatalf("AddInbound: %v", err)
		}
		return created, needRestart
	}

	plain, plainRestart := create(46301, "false")
	if plainRestart {
		t.Fatal("an unrouted naiveproxy inbound must not request an xray restart")
	}
	if _, err := svc.DelInbound(plain.Id); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}

	routed, routedRestart := create(46302, "true")
	if !routedRestart {
		t.Fatal("adding a routed naiveproxy inbound must request a config regen for its bridge")
	}
	if p := routeXrayPortOf(t, routed.Settings); p <= 0 {
		t.Fatalf("the backend must allocate the bridge port, got %d", p)
	}

	if restart, err := svc.SetInboundEnable(routed.Id, false); err != nil || !restart {
		t.Fatalf("disabling a routed inbound must drop its bridge: restart=%v err=%v", restart, err)
	}
	if restart, err := svc.DelInbound(routed.Id); err != nil || !restart {
		t.Fatalf("deleting a routed inbound must drop its bridge: restart=%v err=%v", restart, err)
	}
}

// GetXrayConfig must wire a routed naiveproxy inbound's bridge in, and must not
// emit the sidecar itself as an Xray inbound (only the bridge carries its tag).
func TestGetXrayConfigInjectsNaiveProxyBridge(t *testing.T) {
	setupSettingTestDB(t)
	in := &model.Inbound{
		Tag:      "inbound-18443",
		Enable:   true,
		Port:     18443,
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"n.example.com","certFile":"/c.pem","keyFile":"/k.pem","routeThroughXray":true,"routeXrayPort":50200,"clients":[]}`,
	}
	if err := database.GetDB().Create(in).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}

	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	bridges := 0
	for _, ic := range cfg.InboundConfigs {
		if ic.Tag != in.Tag {
			continue
		}
		bridges++
		if ic.Protocol != "socks" || ic.Port != 50200 {
			t.Fatalf("the inbound carrying the tag must be the SOCKS bridge, got %+v", ic)
		}
	}
	if bridges != 1 {
		t.Fatalf("want exactly one inbound tagged %s (the bridge), got %d", in.Tag, bridges)
	}
}
