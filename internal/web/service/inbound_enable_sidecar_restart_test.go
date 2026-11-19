package service

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// refusingRuntime is a runtime whose AddInbound is refused, as a sidecar's start is: a tproxy engine while Telegram cannot be reached.
type refusingRuntime struct{ fakeNodeRuntime }

func (r *refusingRuntime) AddInbound(context.Context, *model.Inbound) error {
	r.addInbound.Add(1)
	return errors.New("the runtime refused to start the inbound")
}

// A restart of Xray does not start a sidecar, and it drops every live connection of the inbounds Xray does serve.
func TestSetInboundEnableAsksForAnXrayRestartOnlyForAnInboundXrayServes(t *testing.T) {
	cases := []struct {
		name        string
		protocol    model.Protocol
		stream      string
		settings    string
		wantRestart bool
	}{
		{"tproxy", model.Tproxy, ``, `{"clients":[{"email":"tp","tproxySecret":"00112233445566778899aabbccddeeff","enable":true}]}`, false},
		{"mtproto", model.MTProto, ``, `{"clients":[{"email":"mt","secret":"` + mtprotoTestSecretA + `","enable":true}]}`, false},
		{"vless", model.VLESS, `{"network":"tcp"}`, `{}`, true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			setupConflictDB(t)
			refusing := &refusingRuntime{}
			mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }})
			mgr.SetLocalRuntimeOverride(refusing)
			runtime.SetManager(mgr)
			t.Cleanup(func() { runtime.SetManager(nil) })

			seedInboundConflict(t, "refused-"+c.name, "0.0.0.0", 46180+i, c.protocol, c.stream, c.settings)
			row := loadInboundByTag(t, "refused-"+c.name)
			disableInboundRow(t, row.Id)

			needRestart, err := (&InboundService{}).SetInboundEnable(row.Id, true)
			if err != nil {
				t.Fatalf("SetInboundEnable: %v", err)
			}
			if n := refusing.addInbound.Load(); n != 1 {
				t.Fatalf("the runtime was asked to add the inbound %d time(s), want 1: the refusal was never met", n)
			}
			if needRestart != c.wantRestart {
				t.Errorf("needRestart = %v, want %v", needRestart, c.wantRestart)
			}
		})
	}
}
