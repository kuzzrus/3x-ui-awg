package xray

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

func TestApplyHotWithoutTheCore(t *testing.T) {
	withClient := func(cfg *Config) {
		cfg.InboundConfigs[1].Settings = json_util.RawMessage(`{"clients":[{"email":"a","id":"uuid-a"}]}`)
	}
	tests := []struct {
		name          string
		prepare       func(oldCfg, newCfg *Config)
		noPolicy      bool
		restart       bool
		wantApplied   bool
		wantCalls     int
		wantNewConfig bool
	}{
		{
			name:          "identical config is adopted without the core",
			prepare:       func(oldCfg, newCfg *Config) {},
			wantApplied:   true,
			wantNewConfig: true,
		},
		{
			name:    "section without a reload api forces a restart",
			prepare: func(oldCfg, newCfg *Config) { newCfg.LogConfig = json_util.RawMessage(`{"loglevel":"debug"}`) },
		},
		{
			name:      "dropped client with the restart policy on",
			prepare:   func(oldCfg, newCfg *Config) { withClient(oldCfg) },
			restart:   true,
			wantCalls: 1,
		},
		{
			name:      "dropped client with the policy off still needs the core api",
			prepare:   func(oldCfg, newCfg *Config) { withClient(oldCfg) },
			wantCalls: 1,
		},
		{
			name: "no policy at all still needs the core api",
			prepare: func(oldCfg, newCfg *Config) {
				newCfg.InboundConfigs = append(newCfg.InboundConfigs, InboundConfig{
					Port: 2080, Protocol: "vmess", Tag: "inbound-2080", Settings: json_util.RawMessage(`{}`),
				})
			},
			noPolicy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCfg, newCfg := makeHotConfig(), makeHotConfig()
			tt.prepare(oldCfg, newCfg)
			process := NewProcess(oldCfg)

			calls := 0
			var asked *HotDiff
			policy := func(diff *HotDiff) bool {
				calls++
				asked = diff
				return tt.restart
			}
			if tt.noPolicy {
				policy = nil
			}

			if got := ApplyHot(process, newCfg, policy); got != tt.wantApplied {
				t.Fatalf("ApplyHot = %v, want %v", got, tt.wantApplied)
			}
			if calls != tt.wantCalls {
				t.Fatalf("restart policy asked %d times, want %d", calls, tt.wantCalls)
			}
			wantConfig := oldCfg
			if tt.wantNewConfig {
				wantConfig = newCfg
			}
			if process.GetConfig() != wantConfig {
				t.Fatalf("process keeps the wrong config snapshot (new = %v)", process.GetConfig() == newCfg)
			}
			if tt.wantCalls > 0 {
				if len(asked.RemovedUsers) != 1 || asked.RemovedUsers[0].Email != "a" || asked.RemovedUsers[0].Tag != "inbound-1080" {
					t.Fatalf("policy saw diff %+v, want the removal of a@inbound-1080", asked)
				}
				if !asked.DropsUsers() {
					t.Fatal("policy was handed a diff that does not drop users")
				}
			}
		})
	}
}
