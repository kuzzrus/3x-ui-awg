//go:build !windows

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

func TestMain(m *testing.M) {
	// The test binary is symlinked into the bin folder under the core's file name,
	// and lands here when the agent execs it as that name.
	if strings.HasPrefix(filepath.Base(os.Args[0]), "xray-") {
		os.Exit(runFakeXray(os.Args[1:]))
	}

	readySettle = 50 * time.Millisecond
	superviseEvery = 50 * time.Millisecond
	dir, err := os.MkdirTemp("", "agent-test-log")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XUI_LOG_FOLDER", dir)
	logger.InitLogger(logging.ERROR)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// runFakeXray stands in for the core: it answers -version, tests a config, or serves the
// api port until stopped. FAIL_TEST or FAIL_START in the config makes it refuse that step,
// and NO_API makes it run without ever opening the api port.
func runFakeXray(args []string) int {
	testOnly, path := false, ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-version":
			fmt.Println("Xray 9.9.9 (fake)")
			return 0
		case "-test":
			testOnly = true
		case "-c":
			if i+1 < len(args) {
				path = args[i+1]
				i++
			}
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake xray:", err)
		return 1
	}
	if testOnly {
		if bytes.Contains(raw, []byte(markerFailTest)) {
			fmt.Fprintln(os.Stderr, "Failed to start: fake rejects this config")
			return 23
		}
		fmt.Println("Configuration OK.")
		return 0
	}
	if bytes.Contains(raw, []byte(markerFailStart)) {
		fmt.Fprintln(os.Stderr, "Failed to start: fake cannot bind")
		return 1
	}
	if bytes.Contains(raw, []byte(markerNoAPI)) {
		hang := make(chan os.Signal, 1)
		signal.Notify(hang, syscall.SIGTERM, syscall.SIGINT)
		<-hang
		return 0
	}

	var cfg struct {
		Inbounds []struct {
			Tag  string `json:"tag"`
			Port int    `json:"port"`
		} `json:"inbounds"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fmt.Fprintln(os.Stderr, "fake xray:", err)
		return 1
	}
	apiPort := 0
	for _, in := range cfg.Inbounds {
		if in.Tag == "api" {
			apiPort = in.Port
		}
	}
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(apiPort)))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Failed to start:", err)
		return 1
	}
	dir := filepath.Dir(path)
	if f, err := os.OpenFile(filepath.Join(dir, "fake.starts"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
		_, _ = f.WriteString("start\n")
		_ = f.Close()
	}
	_ = os.WriteFile(filepath.Join(dir, "fake.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop
	return 0
}

// waitFor polls until cond holds, failing the test after the timeout.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(timeout); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
