package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
)

const naiveRuntimeSettings = `{"domain":"n.example.com","certFile":"/c.pem","keyFile":"/k.pem",` +
	`"clients":[{"email":"a","naiveProxyPassword":"pw","enable":true}]}`

// newNaiveLocal builds a Local whose front proxy reloads are counted. It has no engine
// installed (an empty bin folder), which is all these tests need: Ensure fails cleanly.
func newNaiveLocal(t *testing.T, reloads *int) *Local {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	return NewLocal(LocalDeps{
		APIPort:          func() int { t.Fatal("a NaiveProxy inbound must not touch the Xray API"); return 0 },
		ReloadFrontProxy: func() { *reloads++ },
	})
}

// The front proxy's SNI relay reads the inbound's domain, so it must hear about every
// change, including one whose Ensure failed.
func TestAddNaiveProxyInboundReloadsFrontProxyEvenWhenTheEngineIsMissing(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	ib := &model.Inbound{Id: 1, Protocol: model.NaiveProxy, Enable: true, Port: 46201, Settings: naiveRuntimeSettings}
	err := l.AddInbound(context.Background(), ib)

	if !errors.Is(err, naiveproxy.ErrNotInstalled) {
		t.Fatalf("AddInbound = %v, want ErrNotInstalled so the caller can tell the admin why", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadFrontProxy calls = %d, want exactly 1", reloads)
	}
}

func TestAddNaiveProxyInboundWithUnusableSettingsStillReloadsFrontProxy(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	ib := &model.Inbound{Id: 2, Protocol: model.NaiveProxy, Enable: true, Port: 46202, Settings: `not json`}
	if err := l.AddInbound(context.Background(), ib); err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadFrontProxy calls = %d, want exactly 1", reloads)
	}
}

func TestUpdateNaiveProxyInboundSwitchedOffReloadsFrontProxy(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	oldIb := &model.Inbound{Id: 3, Protocol: model.NaiveProxy, Enable: true, Port: 46203, Settings: naiveRuntimeSettings}
	newIb := &model.Inbound{Id: 3, Protocol: model.NaiveProxy, Enable: false, Port: 46203, Settings: naiveRuntimeSettings}
	if err := l.UpdateInbound(context.Background(), oldIb, newIb); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadFrontProxy calls = %d, want exactly 1", reloads)
	}
}

// Leaving the protocol must stop the sidecar and drop its domain from the relay, and must not
// try to reach an Xray inbound that never existed for it.
func TestUpdateNaiveProxyInboundMovedToAnotherProtocolReloadsFrontProxy(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	oldIb := &model.Inbound{Id: 4, Protocol: model.NaiveProxy, Enable: true, Port: 46204, Settings: naiveRuntimeSettings}
	newIb := &model.Inbound{Id: 4, Protocol: model.VLESS, Enable: false, Port: 46204}
	if err := l.UpdateInbound(context.Background(), oldIb, newIb); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadFrontProxy calls = %d, want exactly 1", reloads)
	}
}

func TestDelNaiveProxyInboundReloadsFrontProxy(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	ib := &model.Inbound{Id: 5, Protocol: model.NaiveProxy, Enable: true, Port: 46205, Settings: naiveRuntimeSettings}
	if err := l.DelInbound(context.Background(), ib); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}
	if reloads != 1 {
		t.Fatalf("ReloadFrontProxy calls = %d, want exactly 1", reloads)
	}
}

// AddUser/RemoveUser feed the native Xray API; a NaiveProxy inbound has no registered
// inbound there (Caddy authenticates its users), so reaching them must be a no-op.
func TestNaiveProxyProtocolSkipsNativeXrayAPI(t *testing.T) {
	reloads := 0
	l := newNaiveLocal(t, &reloads)

	ib := &model.Inbound{Id: 6, Protocol: model.NaiveProxy, Tag: "inbound-46206"}
	if err := l.AddUser(context.Background(), ib, map[string]any{"email": "x"}); err != nil {
		t.Errorf("AddUser: %v", err)
	}
	if err := l.RemoveUser(context.Background(), ib, "x"); err != nil {
		t.Errorf("RemoveUser: %v", err)
	}
	if reloads != 0 {
		t.Errorf("a client change must not reload the front proxy, got %d reloads", reloads)
	}
}
