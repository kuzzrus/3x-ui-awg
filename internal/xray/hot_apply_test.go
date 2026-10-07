package xray

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/xtls/xray-core/app/proxyman/command"
	"google.golang.org/grpc"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

func withClientA(cfg *Config) {
	cfg.InboundConfigs[1].Settings = json_util.RawMessage(`{"clients":[{"email":"a","id":"uuid-a"}]}`)
}

func TestApplyHotWithoutTheCore(t *testing.T) {
	tests := []struct {
		name          string
		prepare       func(oldCfg, newCfg *Config)
		noPolicy      bool
		wantApplied   bool
		wantAsked     bool
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
			name:      "a dropped client is put to the policy, which says restart",
			prepare:   func(oldCfg, newCfg *Config) { withClientA(oldCfg) },
			wantAsked: true,
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

			var asked *HotDiff
			policy := func(diff *HotDiff) bool {
				asked = diff
				return true
			}
			if tt.noPolicy {
				policy = nil
			}

			if got := ApplyHot(process, newCfg, policy); got != tt.wantApplied {
				t.Fatalf("ApplyHot = %v, want %v", got, tt.wantApplied)
			}
			if (asked != nil) != tt.wantAsked {
				t.Fatalf("restart policy asked = %v, want %v", asked != nil, tt.wantAsked)
			}
			wantConfig := oldCfg
			if tt.wantNewConfig {
				wantConfig = newCfg
			}
			if process.GetConfig() != wantConfig {
				t.Fatalf("process keeps the wrong config snapshot (new = %v)", process.GetConfig() == newCfg)
			}
			if tt.wantAsked {
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

// countingHandler is a core that accepts every change and counts them.
type countingHandler struct {
	command.UnimplementedHandlerServiceServer
	alters atomic.Int32
}

func (h *countingHandler) AlterInbound(context.Context, *command.AlterInboundRequest) (*command.AlterInboundResponse, error) {
	h.alters.Add(1)
	return &command.AlterInboundResponse{}, nil
}

// With a core that answers, the policy's answer is observable: a yes keeps every
// change from reaching it, a no lets the removal through.
func TestApplyHotObeysTheDropPolicy(t *testing.T) {
	handler := &countingHandler{}
	server := grpc.NewServer()
	command.RegisterHandlerServiceServer(server, handler)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(server.Stop)
	port := ln.Addr().(*net.TCPAddr).Port

	tests := []struct {
		name       string
		restart    bool
		wantHot    bool
		wantAlters int32
	}{
		{"the policy says restart", true, false, 0},
		{"the policy says carry on", false, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldCfg, newCfg := makeHotConfig(), makeHotConfig()
			oldCfg.InboundConfigs[0].Port, newCfg.InboundConfigs[0].Port = port, port
			withClientA(oldCfg)
			process := NewProcess(oldCfg)
			process.refreshAPIPort()
			handler.alters.Store(0)

			if got := ApplyHot(process, newCfg, func(*HotDiff) bool { return tt.restart }); got != tt.wantHot {
				t.Fatalf("ApplyHot = %v, want %v", got, tt.wantHot)
			}
			if got := handler.alters.Load(); got != tt.wantAlters {
				t.Fatalf("the core was altered %d times, want %d", got, tt.wantAlters)
			}
			wantConfig := oldCfg
			if tt.wantHot {
				wantConfig = newCfg
			}
			if process.GetConfig() != wantConfig {
				t.Fatalf("process keeps the wrong config snapshot (new = %v)", process.GetConfig() == newCfg)
			}
		})
	}
}
