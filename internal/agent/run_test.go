//go:build !windows

package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// A stored config that never opens the core's api port keeps the start attempt waiting for
// startTimeout. The master must be able to reach the node, and push a fix, meanwhile.
func TestRunAnswersWhileTheStoredConfigIsStillStarting(t *testing.T) {
	f := newFixture(t)
	if err := f.state.SaveLastGood(f.config(markerNoAPI).compact(t), false); err != nil {
		t.Fatal(err)
	}
	previous := startTimeout
	startTimeout = 8 * time.Second
	t.Cleanup(func() { startTimeout = previous })

	port := freePort(t)
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", port, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	pin, err := hex.DecodeString(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := agentproto.ClientTLSConfig(bundle.Secret, pin)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 2 * time.Second}
	t.Cleanup(client.CloseIdleConnections)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	began := time.Now()
	go func() {
		finished <- Run(ctx, Options{Bundle: bundle, StateDir: f.state.dir, LogDir: f.logDir})
	}()

	var status agentproto.Status
	url := "https://127.0.0.1:" + strconv.Itoa(port) + agentproto.PathStatus
	for ; ; time.Sleep(50 * time.Millisecond) {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bundle.Secret)
		if resp, err := client.Do(req); err == nil {
			decodeErr := json.NewDecoder(resp.Body).Decode(&status)
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusOK || decodeErr != nil {
				t.Fatalf("status answered %d, decode error %v", resp.StatusCode, decodeErr)
			}
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("Run ended before it served: %v", err)
		default:
		}
		if time.Since(began) > startTimeout/2 {
			t.Fatalf("the agent did not answer within %s, which is waiting for the start attempt", startTimeout/2)
		}
	}
	if status.XrayState == agentproto.XrayStateRunning {
		t.Fatalf("status = %+v, want the core not running yet", status)
	}

	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("Run returned %v after it was told to stop", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
}
