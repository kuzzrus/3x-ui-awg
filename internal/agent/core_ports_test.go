package agent

import (
	"encoding/json"
	"net"
	"strconv"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// preparedConfig is a config as the agent holds it after it was pushed: the api inbound on apiPort, the metrics
// section on the loopback at metricsPort unless that is 0, and an inbound on each of the ports that follow.
func preparedConfig(t *testing.T, apiPort, metricsPort int, inbounds ...int) *xray.Config {
	t.Helper()
	listen := ""
	if metricsPort != 0 {
		listen = net.JoinHostPort("127.0.0.1", strconv.Itoa(metricsPort))
	}
	return preparedConfigListening(t, apiPort, listen, inbounds...)
}

// preparedConfigListening is preparedConfig with the metrics section listening at listen, whatever address that is.
func preparedConfigListening(t *testing.T, apiPort int, listen string, inbounds ...int) *xray.Config {
	t.Helper()
	base := testConfig{apiPort: apiPort}
	for _, port := range inbounds {
		base.inbounds = append(base.inbounds, map[string]any{
			"tag": "in-" + strconv.Itoa(port), "listen": "0.0.0.0", "port": port, "protocol": "vless",
			"settings": map[string]any{"clients": []any{}, "decryption": "none"},
		})
	}
	value := base.value()
	if listen != "" {
		value["metrics"] = map[string]any{"listen": listen, "tag": "metrics_out"}
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := prepareConfig(body, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// holdPortOf takes a loopback port for the rest of the test, as another Xray on the same server does.
func holdPortOf(t *testing.T, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("holding %d: %v", port, err)
	}
	t.Cleanup(func() { ln.Close() })
}

func apiPortOf(cfg *xray.Config) int {
	for _, in := range cfg.InboundConfigs {
		if in.Tag == apiTag {
			return in.Port
		}
	}
	return 0
}

func TestLocalizeKeepsThePortsOfAConfigWhenTheyAreFree(t *testing.T) {
	api, metrics := freePort(t), freePort(t)
	cfg := preparedConfig(t, api, metrics)
	(&Core{}).localize(cfg)
	if got := apiPortOf(cfg); got != api {
		t.Errorf("api port %d, want the %d of the config", got, api)
	}
	if got := metricsPort(cfg.Metrics); got != metrics {
		t.Errorf("metrics port %d, want the %d of the config", got, metrics)
	}
}

// Another Xray on the server holds the ports of the template, which are the same for every core the master renders.
func TestLocalizeMovesThePortsThatSomethingHolds(t *testing.T) {
	api, metrics := freePort(t), freePort(t)
	holdPortOf(t, api)
	holdPortOf(t, metrics)
	cfg := preparedConfig(t, api, metrics)
	(&Core{}).localize(cfg)

	movedAPI, movedMetrics := apiPortOf(cfg), metricsPort(cfg.Metrics)
	if movedAPI == api || movedMetrics == metrics {
		t.Fatalf("api %d and metrics %d, want others than the held %d and %d", movedAPI, movedMetrics, api, metrics)
	}
	if movedAPI == movedMetrics {
		t.Errorf("both ports are %d", movedAPI)
	}
	for name, port := range map[string]int{"api": movedAPI, "metrics": movedMetrics} {
		if !loopbackPortFree(port) {
			t.Errorf("the %s port %d that was chosen is not free", name, port)
		}
	}
	var section struct{ Listen, Tag string }
	if err := json.Unmarshal(cfg.Metrics, &section); err != nil {
		t.Fatal(err)
	}
	if want := net.JoinHostPort("127.0.0.1", strconv.Itoa(movedMetrics)); section.Listen != want || section.Tag != "metrics_out" {
		t.Errorf("the metrics section is %s, want its listener at %s and the rest as it was", cfg.Metrics, want)
	}
}

func TestLocalizeMovesOnlyThePortThatIsHeld(t *testing.T) {
	api, metrics := freePort(t), freePort(t)
	holdPortOf(t, api)
	cfg := preparedConfig(t, api, metrics)
	(&Core{}).localize(cfg)
	if apiPortOf(cfg) == api {
		t.Error("the api port that is held was kept")
	}
	if got := metricsPort(cfg.Metrics); got != metrics {
		t.Errorf("metrics port %d, want the free %d of the config", got, metrics)
	}
}

// A config that every push renders anew must come out the same, or each push would restart the core.
func TestLocalizeChoosesOnceAndKeepsItsPorts(t *testing.T) {
	api, metrics := freePort(t), freePort(t)
	holdPortOf(t, api)
	core := &Core{}
	first := preparedConfig(t, api, metrics)
	core.localize(first)
	second := preparedConfig(t, api, metrics)
	core.localize(second)
	if apiPortOf(first) != apiPortOf(second) || string(first.Metrics) != string(second.Metrics) {
		t.Errorf("a second config got api %d and metrics %s, the first had %d and %s",
			apiPortOf(second), second.Metrics, apiPortOf(first), first.Metrics)
	}
	if !first.InboundConfigs[0].Equals(&second.InboundConfigs[0]) {
		t.Error("the api inbound of two renderings of the same config differs after they were localized")
	}
}

// An inbound is added to the node later, perhaps on the very port that was chosen for the core when the node had none.
func TestLocalizeChoosesAgainWhenAnInboundComesToUseAPort(t *testing.T) {
	api, metrics := freePort(t), freePort(t)
	holdPortOf(t, api)
	core := &Core{}
	core.localize(preparedConfig(t, api, metrics))
	chosen := core.ports
	if chosen.metrics != metrics {
		t.Fatalf("the free metrics port %d of the config was not kept: %d", metrics, chosen.metrics)
	}

	later := preparedConfig(t, api, metrics, chosen.api, chosen.metrics)
	core.localize(later)
	for _, in := range later.InboundConfigs {
		if in.Tag != apiTag && (in.Port == apiPortOf(later) || in.Port == metricsPort(later.Metrics)) {
			t.Errorf("the core keeps the port %d of the inbound %s (api %d, metrics %d)", in.Port, in.Tag, apiPortOf(later), metricsPort(later.Metrics))
		}
	}
	if apiPortOf(later) == chosen.api || metricsPort(later.Metrics) == chosen.metrics {
		t.Errorf("api %d and metrics %d, want others than the %d and %d that inbounds use now", apiPortOf(later), metricsPort(later.Metrics), chosen.api, chosen.metrics)
	}
}

// Metrics on another address than the loopback is an exposure that the operator asked for.
func TestLocalizeLeavesAMetricsListenerThatIsNotOnTheLoopback(t *testing.T) {
	port := freePort(t)
	holdPortOf(t, port)
	for _, address := range []string{"0.0.0.0", "", "192.0.2.7", "[::]"} {
		listen := net.JoinHostPort(address, strconv.Itoa(port))
		cfg := preparedConfigListening(t, freePort(t), listen)
		core := &Core{}
		core.localize(cfg)
		var section struct{ Listen string }
		if err := json.Unmarshal(cfg.Metrics, &section); err != nil || section.Listen != listen || core.ports.metrics != 0 {
			t.Errorf("a metrics listener at %q came out as %s (port %d chosen)", listen, cfg.Metrics, core.ports.metrics)
		}
	}
	cfg := preparedConfigListening(t, freePort(t), net.JoinHostPort("localhost", strconv.Itoa(port)))
	(&Core{}).localize(cfg)
	if got := metricsPort(cfg.Metrics); got == port || got == 0 {
		t.Errorf("a metrics listener on localhost came out on port %d, want another than the held %d", got, port)
	}
}

func TestLocalizeLeavesAConfigWithNoMetricsWithNone(t *testing.T) {
	cfg := preparedConfig(t, freePort(t), 0)
	core := &Core{}
	core.localize(cfg)
	if len(cfg.Metrics) != 0 {
		t.Errorf("the metrics section %s appeared", cfg.Metrics)
	}
	if core.ports.metrics != 0 {
		t.Errorf("a metrics port %d was chosen for a config that has none", core.ports.metrics)
	}
}

// The ports were free when they were chosen, and nothing keeps them so until the core binds them.
func TestForgetTakenPortsChoosesAgainWhatSomethingCameToHold(t *testing.T) {
	core := &Core{}
	cfg := preparedConfig(t, freePort(t), freePort(t))
	core.localize(cfg)
	chosen := core.ports

	core.forgetTakenPorts()
	if core.ports != chosen {
		t.Fatalf("ports %+v after nothing changed, want %+v", core.ports, chosen)
	}
	holdPortOf(t, chosen.api)
	core.forgetTakenPorts()
	again := preparedConfig(t, chosen.api, chosen.metrics)
	core.localize(again)
	if got := apiPortOf(again); got == chosen.api || !loopbackPortFree(got) {
		t.Errorf("api port %d, want a free one other than the held %d", got, chosen.api)
	}
	if got := metricsPort(again.Metrics); got != chosen.metrics {
		t.Errorf("metrics port %d, want the %d that was still free", got, chosen.metrics)
	}
}

func TestChooseCorePortAvoidsThePortsOfTheInbounds(t *testing.T) {
	preferred := freePort(t)
	if got := chooseCorePort("api", preferred, nil); got != preferred {
		t.Errorf("a free preferred port %d was changed to %d", preferred, got)
	}
	got := chooseCorePort("api", preferred, []int{preferred})
	if got == preferred || !loopbackPortFree(got) {
		t.Errorf("port %d, want a free one other than %d, which an inbound uses", got, preferred)
	}
}

func TestMetricsPortAndItsMove(t *testing.T) {
	for raw, want := range map[string]int{
		`{"listen":"127.0.0.1:11111","tag":"metrics_out"}`: 11111,
		`{"tag":"metrics_out"}`:                            0,
		`{"listen":"11111"}`:                               0,
		`{"listen":"127.0.0.1:70000"}`:                     0,
		`{"listen":"localhost:11111"}`:                     11111,
		`{"listen":"[::1]:11111"}`:                         11111,
		`{"listen":"0.0.0.0:11111"}`:                       0,
		`{"listen":":11111"}`:                              0,
		`{"listen":"192.0.2.7:11111"}`:                     0,
		`null`:                                             0,
		``:                                                 0,
	} {
		if got := metricsPort([]byte(raw)); got != want {
			t.Errorf("metricsPort(%s) = %d, want %d", raw, got, want)
		}
	}
	moved := withMetricsPort([]byte(`{"listen":"localhost:11111","tag":"metrics_out"}`), 4242)
	if string(moved) != `{"listen":"localhost:4242","tag":"metrics_out"}` {
		t.Errorf("the moved section is %s", moved)
	}
	if got := withMetricsPort([]byte(`null`), 4242); string(got) != "null" {
		t.Errorf("a section that is none became %s", got)
	}
}
