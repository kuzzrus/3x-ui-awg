package xray

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
)

func e2eVlessInbound(tag string, port int, clients string) InboundConfig {
	return InboundConfig{
		Port:     port,
		Protocol: "vless",
		Tag:      tag,
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Settings: json_util.RawMessage(fmt.Sprintf(`{"clients":[%s],"decryption":"none"}`, clients)),
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

func waitForPortClosed(t *testing.T, port int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if !portOpen(port) {
			return
		}
	}
	t.Fatalf("port %d is still open", port)
}

// TestApplyHot_E2E drives ApplyHot against a live core started through Process.
// Skipped unless XRAY_E2E_BINARY points at an xray executable, like the other e2e tests.
func TestApplyHot_E2E(t *testing.T) {
	bin := os.Getenv("XRAY_E2E_BINARY")
	if bin == "" {
		t.Skip("set XRAY_E2E_BINARY to an xray binary to run this test")
	}
	if runtime.GOOS == "windows" {
		t.Skip("the binary is linked into the bin folder with a symlink")
	}
	bin, err := filepath.Abs(bin)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XUI_LOG_FOLDER", dir)
	if err := os.Symlink(bin, filepath.Join(dir, GetBinaryName())); err != nil {
		t.Fatal(err)
	}

	apiPort, mainPort, extraPort, latePort, lateMovedPort := freePort(t), freePort(t), freePort(t), freePort(t), freePort(t)
	const userA = `{"email":"a","id":"b831381d-6324-4d53-ad4f-8cda48b30811"}`
	const userC = `{"email":"c","id":"b831381d-6324-4d53-ad4f-8cda48b30813"}`
	build := func(inbounds ...InboundConfig) *Config {
		cfg := makeHotConfig()
		cfg.Metrics = nil
		cfg.InboundConfigs = append([]InboundConfig{{
			Port:     apiPort,
			Protocol: "tunnel",
			Tag:      "api",
			Listen:   json_util.RawMessage(`"127.0.0.1"`),
			Settings: json_util.RawMessage(`{"rewriteAddress":"127.0.0.1"}`),
		}}, inbounds...)
		return cfg
	}

	oldCfg := build(e2eVlessInbound("main", mainPort, userA))
	process := NewProcess(oldCfg)
	if err := process.Start(); err != nil {
		t.Fatalf("start core: %v", err)
	}
	t.Cleanup(func() { _ = process.Stop() })
	waitForPort(t, apiPort)

	api := &XrayAPI{}
	if err := api.Init(apiPort); err != nil {
		t.Fatalf("api init: %v", err)
	}
	t.Cleanup(api.Close)

	// A client is added and a new inbound appears, both without a restart.
	withExtra := build(
		e2eVlessInbound("main", mainPort, userA+","+userC),
		e2eVlessInbound("extra", extraPort, userA),
	)
	if !ApplyHot(process, withExtra, nil) {
		t.Fatal("adding a client and an inbound must apply hot")
	}
	if process.GetConfig() != withExtra {
		t.Fatal("process keeps the old config snapshot after a hot apply")
	}
	waitForPort(t, extraPort)
	err = api.AddUser("vless", "main", panelUser("c", map[string]any{"id": "b831381d-6324-4d53-ad4f-8cda48b30813"}))
	if !IsUserExistsErr(err) {
		t.Fatalf("client c was not added by the hot apply: AddUser err = %v", err)
	}

	// A handler the stored snapshot does not know about is replaced, not rejected.
	stale := fmt.Appendf(nil, `{"listen":"127.0.0.1","port":%d,"protocol":"vless","tag":"late",`+
		`"settings":{"clients":[%s],"decryption":"none"}}`, latePort, userA)
	if err := api.AddInbound(stale); err != nil {
		t.Fatalf("seed stale handler: %v", err)
	}
	withLate := build(
		e2eVlessInbound("main", mainPort, userA+","+userC),
		e2eVlessInbound("extra", extraPort, userA),
		e2eVlessInbound("late", lateMovedPort, userA),
	)
	if !ApplyHot(process, withLate, nil) {
		t.Fatal("an inbound whose tag the core already has must be reconciled")
	}
	waitForPort(t, lateMovedPort)
	waitForPortClosed(t, latePort)

	// A change the core cannot take at runtime is left to the caller's restart.
	withLog := build(
		e2eVlessInbound("main", mainPort, userA+","+userC),
		e2eVlessInbound("extra", extraPort, userA),
		e2eVlessInbound("late", lateMovedPort, userA),
	)
	withLog.LogConfig = json_util.RawMessage(`{"loglevel":"debug"}`)
	if ApplyHot(process, withLog, nil) {
		t.Fatal("a log change has no reload api and must report false")
	}
	if process.GetConfig() != withLate {
		t.Fatal("a refused apply must keep the snapshot of what is running")
	}

	// Dropping a client asks the policy, and a yes leaves the core untouched.
	withoutC := build(
		e2eVlessInbound("main", mainPort, userA),
		e2eVlessInbound("extra", extraPort, userA),
		e2eVlessInbound("late", lateMovedPort, userA),
	)
	asked := false
	if ApplyHot(process, withoutC, func(diff *HotDiff) bool { asked = diff.DropsUsers(); return true }) {
		t.Fatal("the restart policy said restart, ApplyHot must report false")
	}
	if !asked {
		t.Fatal("the policy was not asked about the dropped client")
	}
	if err := api.AddUser("vless", "main", panelUser("c", map[string]any{"id": "b831381d-6324-4d53-ad4f-8cda48b30813"})); !IsUserExistsErr(err) {
		t.Fatalf("client c must still be in the core after a deferred drop: AddUser err = %v", err)
	}

	// Without the policy the same drop is applied through the API.
	if !ApplyHot(process, withoutC, nil) {
		t.Fatal("dropping a client must apply hot when no policy objects")
	}
	if err := api.AddUser("vless", "main", panelUser("c", map[string]any{"id": "b831381d-6324-4d53-ad4f-8cda48b30813"})); err != nil {
		t.Fatalf("client c must be gone after the hot drop, so adding it again succeeds: %v", err)
	}
}
