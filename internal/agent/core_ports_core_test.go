//go:build !windows

package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

// withMetrics is a config of the fixture whose metrics section listens on port, as the template of the master has it.
func (f *fixture) withMetrics(mark string, port int) []byte {
	f.t.Helper()
	value := f.config(mark).value()
	value["metrics"] = map[string]any{"listen": net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), "tag": "metrics_out"}
	body, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	return body
}

// startedWith is the api and metrics ports of the config the core was last started with.
func (f *fixture) startedWith() (api, metrics int) {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.binDir, "config.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var cfg struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
		Metrics json_util.RawMessage `json:"metrics"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		f.t.Fatal(err)
	}
	for _, in := range cfg.Inbounds {
		if in.Tag == apiTag {
			api = in.Port
		}
	}
	return api, metricsPort(cfg.Metrics)
}

func (f *fixture) wantRunning() {
	f.t.Helper()
	if state := f.core.Snapshot().XrayState; state != agentproto.XrayStateRunning {
		f.t.Fatalf("the core is %s, want it running (%s)", state, f.core.Snapshot().XrayError)
	}
}

// The master renders every core from one template, so a server with a panel of its own holds the ports of it already:
// the agent must not wait for them to be free, which they may never be.
func TestCoreStartsOnOtherPortsWhenThoseOfTheConfigAreHeld(t *testing.T) {
	f := newFixture(t)
	metrics := freePort(t)
	holdPortOf(t, f.port)
	holdPortOf(t, metrics)

	f.mustApply(f.withMetrics("a", metrics))
	f.wantRunning()
	api, movedMetrics := f.startedWith()
	if api == f.port || movedMetrics == metrics || api == 0 || movedMetrics == 0 {
		t.Fatalf("the core runs with api %d and metrics %d, want others than the held %d and %d", api, movedMetrics, f.port, metrics)
	}
}

// The ports of the core are no part of what a push changes, or each push would restart it.
func TestCoreKeepsItsPortsAcrossPushes(t *testing.T) {
	f := newFixture(t)
	metrics := freePort(t)
	holdPortOf(t, f.port)

	f.mustApply(f.withMetrics("a", metrics))
	api, movedMetrics := f.startedWith()
	startsBefore := f.starts()

	// The same config renders again, and then one that the core can only take by restarting.
	if resp := f.mustApply(f.withMetrics("a", metrics)); resp.Applied != agentproto.AppliedNoop {
		t.Errorf("the same config was %s, want a noop", resp.Applied)
	}
	changed := f.withMetrics("b", metrics)
	if resp := f.mustApply(changed); resp.Applied != agentproto.AppliedRestart {
		t.Errorf("a changed env was %s, want a restart", resp.Applied)
	}
	// The same config under another revision, which only the flag of the push changes: the running core has nothing to take.
	resp, err := f.core.Apply(context.Background(), changed, true)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if resp.Applied != agentproto.AppliedNoop {
		t.Errorf("the same config under another revision was %s, want a noop", resp.Applied)
	}
	if f.starts() != startsBefore+1 {
		t.Errorf("the core started %d times, want one more than the %d it had", f.starts(), startsBefore)
	}
	if again, againMetrics := f.startedWith(); again != api || againMetrics != movedMetrics {
		t.Errorf("after a restart api %d and metrics %d, want the %d and %d it had", again, againMetrics, api, movedMetrics)
	}
}

// A port that was free when it was chosen can be taken before the core is started again.
func TestCoreChoosesAgainWhenAPortIsTakenBeforeItStartsAgain(t *testing.T) {
	f := newFixture(t)
	metrics := freePort(t)
	f.mustApply(f.withMetrics("a", metrics))
	api, _ := f.startedWith()

	f.core.stopProcess()
	holdPortOf(t, api)
	if err := f.core.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	f.wantRunning()
	if again, _ := f.startedWith(); again == api {
		t.Errorf("the core was started on the api port %d that something holds", api)
	}
}
