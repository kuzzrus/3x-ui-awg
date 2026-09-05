package psiphon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func writeNotices(t *testing.T, lines ...string) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(NoticesPath(), []byte(content), 0o600); err != nil {
		t.Fatalf("writing notices fixture: %v", err)
	}
}

func TestCurrentTunnelMissingFile(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	status, err := CurrentTunnel()
	if err != nil {
		t.Fatalf("CurrentTunnel with no log file: %v", err)
	}
	if status != (TunnelStatus{}) {
		t.Errorf("CurrentTunnel with no log file = %+v, want a zero TunnelStatus", status)
	}
}

func TestCurrentTunnelParsesLatestOfEach(t *testing.T) {
	writeNotices(t,
		`{"noticeType":"ClientRegion","data":{"region":"LV"}}`,
		`{"noticeType":"Tunnels","data":{"count":0}}`,
		`{"noticeType":"Tunnels","data":{"count":1}}`,
		`{"noticeType":"ConnectedServerRegion","data":{"serverRegion":"BE"}}`,
		`{"noticeType":"ConnectedServerRegion","data":{"serverRegion":"JP"}}`,
	)
	status, err := CurrentTunnel()
	if err != nil {
		t.Fatalf("CurrentTunnel: %v", err)
	}
	want := TunnelStatus{Connected: true, TunnelCount: 1, ServerRegion: "JP", ClientRegion: "LV"}
	if status != want {
		t.Errorf("CurrentTunnel = %+v, want %+v", status, want)
	}
}

func TestCurrentTunnelZeroCountMeansNotConnected(t *testing.T) {
	writeNotices(t, `{"noticeType":"Tunnels","data":{"count":0}}`)
	status, err := CurrentTunnel()
	if err != nil {
		t.Fatalf("CurrentTunnel: %v", err)
	}
	if status.Connected {
		t.Errorf("CurrentTunnel.Connected = true for a Tunnels count of 0")
	}
}

func TestCurrentTunnelSkipsMalformedLines(t *testing.T) {
	writeNotices(t,
		`not even json`,
		`{"noticeType":"Tunnels","data":{"count":1}}`,
		``,
		`{this is not valid json either`,
	)
	status, err := CurrentTunnel()
	if err != nil {
		t.Fatalf("CurrentTunnel with malformed lines mixed in: %v", err)
	}
	if !status.Connected || status.TunnelCount != 1 {
		t.Errorf("CurrentTunnel = %+v, want the one well-formed notice still picked up", status)
	}
}

// Tunnels and ClientRegion fire once near the start of a run and never
// again, so CurrentTunnel must still find them deep inside a large log.
func TestCurrentTunnelFindsNoticeFarFromEndOfLargeLog(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		t.Fatalf("creating config dir: %v", err)
	}
	early := `{"noticeType":"Tunnels","data":{"count":1}}` + "\n" +
		`{"noticeType":"ClientRegion","data":{"region":"LV"}}` + "\n"
	padding := strings.Repeat(`{"noticeType":"Ignored","data":{}}`+"\n", 20000)
	content := early + padding
	if len(padding) < 512<<10 {
		t.Fatalf("test fixture padding (%d bytes) is not large enough to exercise this", len(padding))
	}
	if err := os.WriteFile(NoticesPath(), []byte(content), 0o600); err != nil {
		t.Fatalf("writing notices fixture: %v", err)
	}

	status, err := CurrentTunnel()
	if err != nil {
		t.Fatalf("CurrentTunnel: %v", err)
	}
	if !status.Connected || status.TunnelCount != 1 || status.ClientRegion != "LV" {
		t.Errorf("CurrentTunnel over a large log = %+v, want the early Tunnels/ClientRegion notices still picked up", status)
	}
}

func TestRetryExitSucceedsFirstTry(t *testing.T) {
	calls := 0
	want := ExitInfo{IP: "1.2.3.4", Country: "IT"}
	got, err := retryExit(context.Background(), time.Second, time.Millisecond, func() (ExitInfo, error) {
		calls++
		return want, nil
	})
	if err != nil {
		t.Fatalf("retryExit: %v", err)
	}
	if got != want {
		t.Errorf("retryExit = %+v, want %+v", got, want)
	}
	if calls != 1 {
		t.Errorf("retryExit called fn %d times on an immediate success, want 1", calls)
	}
}

// The exact scenario this exists for: the tunnel isn't up yet when Verify is
// first clicked (the SOCKS "general failure" case), then connects a couple
// of retries later, well inside the budget.
func TestRetryExitSucceedsAfterFailures(t *testing.T) {
	calls := 0
	want := ExitInfo{IP: "5.6.7.8", Country: "IN"}
	got, err := retryExit(context.Background(), time.Second, time.Millisecond, func() (ExitInfo, error) {
		calls++
		if calls < 3 {
			return ExitInfo{}, errors.New("general SOCKS server failure")
		}
		return want, nil
	})
	if err != nil {
		t.Fatalf("retryExit: %v", err)
	}
	if got != want {
		t.Errorf("retryExit = %+v, want %+v", got, want)
	}
	if calls != 3 {
		t.Errorf("retryExit called fn %d times, want exactly 3 (2 failures + 1 success)", calls)
	}
}

func TestRetryExitReturnsLastErrorAfterBudgetExhausted(t *testing.T) {
	calls := 0
	_, err := retryExit(context.Background(), 20*time.Millisecond, 5*time.Millisecond, func() (ExitInfo, error) {
		calls++
		return ExitInfo{}, fmt.Errorf("attempt %d failed", calls)
	})
	if err == nil {
		t.Fatal("retryExit with an always-failing fn returned nil error, want the last failure")
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("attempt %d failed", calls)) {
		t.Errorf("retryExit error = %q, want it to be the last attempt's own error (attempt %d)", err.Error(), calls)
	}
	if calls < 2 {
		t.Errorf("retryExit called fn only %d time(s) over a 20ms budget with a 5ms interval, want at least 2", calls)
	}
}

func TestRetryExitRespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	done := make(chan struct{})
	var err error
	go func() {
		_, err = retryExit(ctx, time.Minute, 10*time.Millisecond, func() (ExitInfo, error) {
			calls++
			if calls == 1 {
				cancel()
			}
			return ExitInfo{}, errors.New("still failing")
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("retryExit did not return promptly after ctx was cancelled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("retryExit error = %v, want context.Canceled", err)
	}
}
