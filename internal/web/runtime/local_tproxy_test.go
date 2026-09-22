package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestUpdateTproxyInboundReloadsFrontProxy(t *testing.T) {
	calls := 0
	l := NewLocal(LocalDeps{
		APIPort:          func() int { return 0 },
		TproxyDomain:     func() (string, error) { return "proxy.example.com", nil },
		ReloadFrontProxy: func() { calls++ },
	})

	// Enable=false stays on the cheap Manager.Remove path -- Ensure needs
	// linux/amd64, which this dev machine cannot exercise anyway.
	oldIb := &model.Inbound{Id: 1, Protocol: model.Tproxy, Enable: false}
	newIb := &model.Inbound{Id: 1, Protocol: model.Tproxy, Enable: false}
	if err := l.UpdateInbound(context.Background(), oldIb, newIb); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected ReloadFrontProxy to be called exactly once, got %d", calls)
	}
}

func TestDelTproxyInboundReloadsFrontProxy(t *testing.T) {
	calls := 0
	l := NewLocal(LocalDeps{
		APIPort:          func() int { return 0 },
		TproxyDomain:     func() (string, error) { return "proxy.example.com", nil },
		ReloadFrontProxy: func() { calls++ },
	})

	ib := &model.Inbound{Id: 2, Protocol: model.Tproxy, Enable: true}
	if err := l.DelInbound(context.Background(), ib); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected ReloadFrontProxy to be called exactly once, got %d", calls)
	}
}

// A hostname lookup failure must surface as a real error, not silently skip
// the update while still reloading frontproxy with stale data.
func TestUpdateTproxyInboundPropagatesHostnameError(t *testing.T) {
	reloaded := false
	wantErr := errors.New("db unavailable")
	l := NewLocal(LocalDeps{
		APIPort:          func() int { return 0 },
		TproxyDomain:     func() (string, error) { return "", wantErr },
		ReloadFrontProxy: func() { reloaded = true },
	})

	ib := &model.Inbound{Id: 3, Protocol: model.Tproxy, Enable: true}
	err := l.UpdateInbound(context.Background(), ib, ib)
	if !errors.Is(err, wantErr) {
		t.Fatalf("UpdateInbound error = %v, want %v", err, wantErr)
	}
	if reloaded {
		t.Error("ReloadFrontProxy must not run when the hostname lookup itself failed")
	}
}

// AddUser/RemoveUser feed the native Xray API; tproxy has no registered
// inbound there, so either reaching them must be a no-op, not a real call.
func TestTproxyProtocolSkipsNativeXrayAPI(t *testing.T) {
	l := NewLocal(LocalDeps{
		APIPort: func() int { t.Fatal("must not touch the Xray API for a tproxy inbound"); return 0 },
	})
	ib := &model.Inbound{Id: 4, Protocol: model.Tproxy, Tag: "in-443-any"}
	if err := l.AddUser(context.Background(), ib, map[string]any{"email": "x"}); err != nil {
		t.Errorf("AddUser: %v", err)
	}
	if err := l.RemoveUser(context.Background(), ib, "x"); err != nil {
		t.Errorf("RemoveUser: %v", err)
	}
}
