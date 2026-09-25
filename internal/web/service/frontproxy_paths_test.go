package service

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	xuilogger "github.com/mhsanaei/3x-ui/v3/internal/logger"
)

var frontProxyPathLoggerOnce sync.Once

func setupFrontProxyPathDB(t *testing.T) {
	t.Helper()
	frontProxyPathLoggerOnce.Do(func() { xuilogger.InitLogger(logging.ERROR) })
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := database.InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() {
		if err := database.CloseDB(); err != nil {
			t.Logf("CloseDB warning: %v", err)
		}
	})
}

func mustCreateInbound(t *testing.T, ib *model.Inbound) *model.Inbound {
	t.Helper()
	if err := database.GetDB().Create(ib).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	return ib
}

func TestFrontProxyPathServiceSetAllAndGetAll(t *testing.T) {
	setupFrontProxyPathDB(t)
	cdn := mustCreateInbound(t, &model.Inbound{Tag: "cdn-xhttp", Listen: "127.0.0.1", Port: 18082, Enable: true, Protocol: model.VLESS})
	ws := mustCreateInbound(t, &model.Inbound{Tag: "cdn-ws", Listen: "", Port: 18081, Enable: true, Protocol: model.VLESS})

	svc := &FrontProxyPathService{}
	err := svc.SetAll([]FrontProxyPathInput{
		{ChildId: cdn.Id, Path: "/xh-cdn-K7m4Qp9s/", SortOrder: 0},
		{ChildId: ws.Id, Path: "ws-cdn-8gK4mN2q", SortOrder: 1},
	}, "/panel-secret/", "/sub-secret/")
	if err != nil {
		t.Fatalf("SetAll: %v", err)
	}

	rows, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	// Stored with surrounding slashes trimmed -- matchesPrefix's own trimming
	// convention, so a cosmetic slash spelling in the input can't produce two
	// rows that resolve differently later.
	if rows[0].Path != "xh-cdn-K7m4Qp9s" || rows[1].Path != "ws-cdn-8gK4mN2q" {
		t.Errorf("paths = %q, %q, want trimmed of surrounding slashes", rows[0].Path, rows[1].Path)
	}
}

func TestFrontProxyPathServiceSetAllReplacesWholeList(t *testing.T) {
	setupFrontProxyPathDB(t)
	a := mustCreateInbound(t, &model.Inbound{Tag: "route-a", Listen: "127.0.0.1", Port: 1111, Enable: true, Protocol: model.VLESS})
	b := mustCreateInbound(t, &model.Inbound{Tag: "route-b", Listen: "127.0.0.1", Port: 2222, Enable: true, Protocol: model.VLESS})

	svc := &FrontProxyPathService{}
	if err := svc.SetAll([]FrontProxyPathInput{{ChildId: a.Id, Path: "/a"}}, "/p/", "/s/"); err != nil {
		t.Fatalf("first SetAll: %v", err)
	}
	if err := svc.SetAll([]FrontProxyPathInput{{ChildId: b.Id, Path: "/b"}}, "/p/", "/s/"); err != nil {
		t.Fatalf("second SetAll: %v", err)
	}

	rows, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(rows) != 1 || rows[0].Path != "b" {
		t.Fatalf("got %+v, want exactly the second call's one row -- SetAll must replace, not append", rows)
	}
}

func TestFrontProxyPathServiceSetAllRejectsInvalidRows(t *testing.T) {
	setupFrontProxyPathDB(t)
	ib := mustCreateInbound(t, &model.Inbound{Listen: "127.0.0.1", Port: 1111, Enable: true, Protocol: model.VLESS})
	svc := &FrontProxyPathService{}

	cases := []struct {
		name  string
		items []FrontProxyPathInput
	}{
		{"empty path", []FrontProxyPathInput{{ChildId: ib.Id, Path: ""}}},
		{"no inbound selected", []FrontProxyPathInput{{ChildId: 0, Path: "/x"}}},
		{"duplicate path", []FrontProxyPathInput{{ChildId: ib.Id, Path: "/x"}, {ChildId: ib.Id, Path: "/x"}}},
		{"collides with panel base path", []FrontProxyPathInput{{ChildId: ib.Id, Path: "/panel-secret/inner"}}},
		{"collides with sub path exactly", []FrontProxyPathInput{{ChildId: ib.Id, Path: "/sub-secret"}}},
		{"collides with reserved api/v1", []FrontProxyPathInput{{ChildId: ib.Id, Path: "/api/v1/foo"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := svc.SetAll(tc.items, "/panel-secret/", "/sub-secret/"); err == nil {
				t.Errorf("SetAll accepted %+v, want a validation error", tc.items)
			}
		})
	}

	// None of the rejected calls above must have left anything behind --
	// SetAll validates before its transaction ever starts.
	rows, err := svc.GetAll()
	if err != nil {
		t.Fatalf("GetAll: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows after every SetAll call was rejected, want 0", len(rows))
	}
}

func TestFrontProxyPathServiceBuildTargetsResolvesLoopback(t *testing.T) {
	setupFrontProxyPathDB(t)
	wildcard := mustCreateInbound(t, &model.Inbound{Tag: "wildcard-listen", Listen: "0.0.0.0", Port: 18082, Enable: true, Protocol: model.VLESS})
	blank := mustCreateInbound(t, &model.Inbound{Tag: "blank-listen", Listen: "", Port: 18081, Enable: true, Protocol: model.VLESS})

	svc := &FrontProxyPathService{}
	if err := svc.SetAll([]FrontProxyPathInput{
		{ChildId: wildcard.Id, Path: "/cdn-a"},
		{ChildId: blank.Id, Path: "/cdn-b"},
	}, "/p/", "/s/"); err != nil {
		t.Fatalf("SetAll: %v", err)
	}

	targets, err := svc.BuildTargets(nil)
	if err != nil {
		t.Fatalf("BuildTargets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	byPath := map[string]int{}
	for _, tg := range targets {
		byPath[tg.Path] = tg.Port
	}
	if byPath["cdn-a"] != 18082 || byPath["cdn-b"] != 18081 {
		t.Errorf("targets = %+v, want ports 18082/18081 for cdn-a/cdn-b regardless of 0.0.0.0/blank Listen", byPath)
	}
}

// A path-routed target only ever gets dialed as 127.0.0.1:port
// (newLoopbackProxy's own hardcoded host) -- a child inbound explicitly
// listening somewhere else would silently route to the wrong host if it
// weren't excluded here.
func TestFrontProxyPathServiceBuildTargetsSkipsNonLoopbackListen(t *testing.T) {
	setupFrontProxyPathDB(t)
	remote := mustCreateInbound(t, &model.Inbound{Listen: "203.0.113.5", Port: 18082, Enable: true, Protocol: model.VLESS})

	svc := &FrontProxyPathService{}
	if err := svc.SetAll([]FrontProxyPathInput{{ChildId: remote.Id, Path: "/cdn"}}, "/p/", "/s/"); err != nil {
		t.Fatalf("SetAll: %v", err)
	}
	targets, err := svc.BuildTargets(nil)
	if err != nil {
		t.Fatalf("BuildTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("got %+v, want the non-loopback-listen row skipped", targets)
	}
}

// A disabled inbound must not be reachable through a path route it was
// never actually removed from -- same "disabled means gone" expectation
// every other inbound-driven feature in this codebase already has.
func TestFrontProxyPathServiceBuildTargetsSkipsDisabledInbound(t *testing.T) {
	setupFrontProxyPathDB(t)
	disabled := mustCreateInbound(t, &model.Inbound{Listen: "127.0.0.1", Port: 18082, Enable: false, Protocol: model.VLESS})

	svc := &FrontProxyPathService{}
	if err := svc.SetAll([]FrontProxyPathInput{{ChildId: disabled.Id, Path: "/cdn"}}, "/p/", "/s/"); err != nil {
		t.Fatalf("SetAll: %v", err)
	}
	targets, err := svc.BuildTargets(nil)
	if err != nil {
		t.Fatalf("BuildTargets: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("got %+v, want the disabled inbound's row skipped", targets)
	}
}
