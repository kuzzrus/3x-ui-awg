package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

func TestRunPrintsTheVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"-version"}, &out); err != nil {
		t.Fatal(err)
	}
	if want := config.GetPanelVersion() + "\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestRunNeedsAUsableBundle(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("not a bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		file string
		want string
	}{
		{"no file", filepath.Join(dir, "missing"), "read the pairing bundle"},
		{"not a bundle", garbage, "unknown format"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := run(context.Background(), []string{"-bundle-file", tt.file}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

func TestRunServesTheMasterUntilStopped(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", port, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	token, err := bundle.Encode()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bundleFile := filepath.Join(dir, "bundle")
	if err := os.WriteFile(bundleFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// run points the core's folders at the flags through the environment.
	t.Setenv("XUI_BIN_FOLDER", "")
	t.Setenv("XUI_LOG_FOLDER", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- run(ctx, []string{
			"-bundle-file", bundleFile,
			"-state-dir", filepath.Join(dir, "state"),
			"-bin-dir", filepath.Join(dir, "bin"),
			"-log-dir", filepath.Join(dir, "log"),
		}, &bytes.Buffer{})
	}()

	pin, err := hex.DecodeString(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := agentproto.ClientTLSConfig(bundle.Secret, pin)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	var status agentproto.Status
	url := "https://127.0.0.1:" + strconv.Itoa(port) + agentproto.PathStatus
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bundle.Secret)
		resp, err := client.Do(req)
		if err == nil {
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || decodeErr != nil {
				t.Fatalf("status endpoint answered %d, decode error %v", resp.StatusCode, decodeErr)
			}
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("run ended before it served: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the agent never answered: %v", err)
		}
	}
	if status.Guid == "" || status.XrayState != agentproto.XrayStateStopped || status.ConfigRevision != "" {
		t.Fatalf("status of a fresh agent = %+v, want an identity and no core", status)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("run returned %v after it was told to stop", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("run did not return after its context ended")
	}
}
