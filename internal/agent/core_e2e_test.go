//go:build !windows

package agent

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func vlessInbound(tag string, port int, clients ...string) map[string]any {
	list := make([]any, 0, len(clients))
	for _, email := range clients {
		list = append(list, map[string]any{"email": email, "id": "b831381d-6324-4d53-ad4f-8cda48b30811"})
	}
	return map[string]any{
		"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": "vless",
		"settings": map[string]any{"clients": list, "decryption": "none"},
	}
}

func portOpen(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// TestCoreE2E drives a Core against a real xray. Skipped unless XRAY_E2E_BINARY
// points at an xray executable, like the other end-to-end tests.
func TestCoreE2E(t *testing.T) {
	real := os.Getenv("XRAY_E2E_BINARY")
	if real == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray binary to run this test")
	}
	real, err := filepath.Abs(real)
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	if err := os.Symlink(real, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	logDir := t.TempDir()
	core := NewCore(state, logDir)
	t.Cleanup(core.Close)
	ctx := context.Background()

	apiPort, mainPort, extraPort := freePort(t), freePort(t), freePort(t)
	cfg := testConfig{apiPort: apiPort, inbounds: []map[string]any{vlessInbound("main", mainPort, "a")}}

	startedAt := func() int64 {
		stats, err := core.Stats()
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		return stats.XrayStartedAt
	}

	first, err := core.Apply(ctx, cfg.compact(t), false)
	if err != nil || first.Applied != agentproto.AppliedRestart || first.XrayState != agentproto.XrayStateRunning {
		t.Fatalf("first apply = %+v, %v; want a restart into a running core", first, err)
	}
	firstStart := startedAt()
	if firstStart == 0 {
		t.Fatal("stats report no start time for a running core")
	}

	// A client and an inbound are added without a restart.
	cfg.inbounds = []map[string]any{vlessInbound("main", mainPort, "a", "c"), vlessInbound("extra", extraPort, "a")}
	hot, err := core.Apply(ctx, cfg.compact(t), false)
	if err != nil || hot.Applied != agentproto.AppliedHot {
		t.Fatalf("second apply = %+v, %v; want a hot apply", hot, err)
	}
	waitFor(t, 5*time.Second, "the new inbound to listen", func() bool { return portOpen(extraPort) })
	if got := startedAt(); got != firstStart {
		t.Fatalf("a hot apply replaced the core: start time %d, then %d", firstStart, got)
	}

	// A log level has no reload API, so the core restarts.
	cfg.logLevel = "info"
	restarted, err := core.Apply(ctx, cfg.compact(t), false)
	if err != nil || restarted.Applied != agentproto.AppliedRestart {
		t.Fatalf("third apply = %+v, %v; want a restart", restarted, err)
	}
	secondStart := startedAt()
	if secondStart <= firstStart {
		t.Fatalf("start time %d after a restart, want it later than %d", secondStart, firstStart)
	}
	waitFor(t, 5*time.Second, "the extra inbound to listen after the restart", func() bool { return portOpen(extraPort) })
	good := cfg.compact(t)

	// The core's own test refuses a VLESS inbound without decryption, and nothing changes.
	broken := cfg
	broken.inbounds = []map[string]any{{
		"tag": "main", "listen": "127.0.0.1", "port": mainPort, "protocol": "vless",
		"settings": map[string]any{"clients": []any{}},
	}}
	_, err = core.Apply(ctx, broken.compact(t), false)
	wantConfigError(t, err, "decryption")
	if got := startedAt(); got != secondStart {
		t.Fatalf("a refused config replaced the core: start time %d, then %d", secondStart, got)
	}

	// A port somebody else holds passes the test but not the start; the old config returns.
	squatter, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer squatter.Close()
	taken := cfg
	taken.inbounds = append(append([]map[string]any{}, cfg.inbounds...),
		vlessInbound("taken", squatter.Addr().(*net.TCPAddr).Port, "a"))
	_, err = core.Apply(ctx, taken.compact(t), false)
	wantConfigError(t, err, "xray")
	snap := core.Snapshot()
	if snap.XrayState != agentproto.XrayStateRunning || snap.Revision != agentproto.RevisionOf(good, false) {
		t.Fatalf("snapshot after the failed start = %+v, want the previous config running again", snap)
	}
	waitFor(t, 5*time.Second, "the old config to serve again", func() bool { return portOpen(extraPort) })

	// A new agent process picks up where this one left off.
	core.Close()
	waitFor(t, 5*time.Second, "the first core to release its ports", func() bool { return !portOpen(extraPort) })
	booted := NewCore(state, logDir)
	t.Cleanup(booted.Close)
	if err := booted.Boot(ctx); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if got := booted.Snapshot(); got.XrayState != agentproto.XrayStateRunning || got.Revision != agentproto.RevisionOf(good, false) {
		t.Fatalf("snapshot after boot = %+v, want the last good config running", got)
	}
	waitFor(t, 5*time.Second, "the booted core to listen", func() bool { return portOpen(extraPort) })
}
