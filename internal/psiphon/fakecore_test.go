//go:build !windows

package psiphon

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// The tests that need a tunnel-core process run this test binary as one: BinPath points at a copy of it, and
// PSIPHON_FAKE_CORE=1 in the environment sends it here instead of into the tests. The fake does what the manager
// can see of the real one: it binds the SOCKS port the config names, writes the notices a tunnel writes, and ends
// on SIGTERM.
const (
	fakeCoreEnv      = "PSIPHON_FAKE_CORE"
	fakeNoTunnelEnv  = "PSIPHON_FAKE_NO_TUNNEL"  // "1": bind the port but never report a tunnel
	fakeExitAfterEnv = "PSIPHON_FAKE_EXIT_AFTER" // a duration: end with a failure after it, as a crash
	fakeFailStartEnv = "PSIPHON_FAKE_FAIL_START" // "1": end at once without binding anything
)

func TestMain(m *testing.M) {
	if os.Getenv(fakeCoreEnv) == "1" {
		os.Exit(runFakeCore(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func runFakeCore(args []string) int {
	var configPath, noticesPath string
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-config":
			configPath = args[i+1]
		case "-notices":
			noticesPath = args[i+1]
		}
	}
	if os.Getenv(fakeFailStartEnv) == "1" {
		fmt.Fprintln(os.Stderr, "fake psiphon: refusing to start")
		return 1
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake psiphon:", err)
		return 1
	}
	var cfg struct {
		LocalSocksProxyPort int
		EgressRegion        string
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fake psiphon:", err)
		return 1
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.LocalSocksProxyPort)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake psiphon:", err)
		return 1
	}
	notices, err := os.OpenFile(noticesPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake psiphon:", err)
		return 1
	}
	notice := func(kind string, data any) {
		line, _ := json.Marshal(map[string]any{"noticeType": kind, "data": data})
		_, _ = notices.Write(append(line, '\n'))
	}
	notice("ListeningSocksProxyPort", map[string]any{"port": cfg.LocalSocksProxyPort})
	if os.Getenv(fakeNoTunnelEnv) != "1" {
		region := cfg.EgressRegion
		if region == "" {
			region = "XX"
		}
		notice("Tunnels", map[string]any{"count": 1})
		notice("ConnectedServerRegion", map[string]any{"serverRegion": region})
	}

	if after := os.Getenv(fakeExitAfterEnv); after != "" {
		if d, err := time.ParseDuration(after); err == nil {
			go func() {
				time.Sleep(d)
				fmt.Fprintln(os.Stderr, "fake psiphon: crashed")
				os.Exit(3)
			}()
		}
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	<-stop
	_ = listener.Close()
	return 0
}

// useFakeCore points the package at a bin folder of its own whose tunnel-core is the fake, and gives it a base
// config. Every process the manager starts from then on is the fake.
func useFakeCore(t *testing.T) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(BinPath(), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o750)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := SaveConfig([]byte(`{"PropagationChannelId":"test","SponsorId":"test","EgressRegion":""}`)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	t.Setenv(fakeCoreEnv, "1")
}

// shortenSupervision makes the waits of the supervisor short enough to see in a test, and puts them back.
func shortenSupervision(t *testing.T, backoff, stable time.Duration) {
	t.Helper()
	oldBackoff, oldMax, oldStable := restartBackoff, maxRestartBackoff, stableAfter
	restartBackoff, maxRestartBackoff, stableAfter = backoff, 8*backoff, stable
	t.Cleanup(func() { restartBackoff, maxRestartBackoff, stableAfter = oldBackoff, oldMax, oldStable })
}
