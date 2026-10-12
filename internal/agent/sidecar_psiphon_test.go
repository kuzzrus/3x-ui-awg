package agent

import (
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

func TestTunnelSummaryDescribesHowTheTunnelStands(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for name, tc := range map[string]struct {
		tunnel psiphon.TunnelStatus
		want   string
	}{
		"not connected yet":   {psiphon.TunnelStatus{}, "connecting"},
		"connected":           {psiphon.TunnelStatus{Connected: true, TunnelCount: 1}, "tunnel up"},
		"connected to a site": {psiphon.TunnelStatus{Connected: true, TunnelCount: 1, ServerRegion: "IT"}, "tunnel up, server in IT"},
	} {
		t.Run(name, func(t *testing.T) {
			var summary tunnelSummary
			got, err := summary.get(now, func() (psiphon.TunnelStatus, error) { return tc.tunnel, nil })
			if err != nil || got != tc.want {
				t.Fatalf("get = %q, %v, want %q", got, err, tc.want)
			}
		})
	}
}

// The notice log grows for as long as the process runs, and every status request asks.
func TestTunnelSummaryReadsTheNoticeLogOnceInAWhile(t *testing.T) {
	reads := 0
	tunnel := psiphon.TunnelStatus{}
	read := func() (psiphon.TunnelStatus, error) {
		reads++
		return tunnel, nil
	}
	var summary tunnelSummary
	now := time.Unix(1_700_000_000, 0)

	if got, _ := summary.get(now, read); got != "connecting" || reads != 1 {
		t.Fatalf("first get = %q after %d reads, want connecting after one", got, reads)
	}
	tunnel = psiphon.TunnelStatus{Connected: true}
	if got, _ := summary.get(now.Add(tunnelSummaryEvery-time.Second), read); got != "connecting" || reads != 1 {
		t.Fatalf("get inside the wait = %q after %d reads, want the old text and no read", got, reads)
	}
	if got, _ := summary.get(now.Add(tunnelSummaryEvery), read); got != "tunnel up" || reads != 2 {
		t.Fatalf("get after the wait = %q after %d reads, want the new text after a second read", got, reads)
	}

	// A new process makes what was read untrue at once.
	tunnel = psiphon.TunnelStatus{}
	summary.forget()
	if got, _ := summary.get(now.Add(tunnelSummaryEvery), read); got != "connecting" || reads != 3 {
		t.Fatalf("get after forget = %q after %d reads, want a new read", got, reads)
	}
}

func TestTunnelSummaryTriesAgainAfterAFailedRead(t *testing.T) {
	var summary tunnelSummary
	now := time.Unix(1_700_000_000, 0)
	if got, err := summary.get(now, func() (psiphon.TunnelStatus, error) { return psiphon.TunnelStatus{}, errors.New("unreadable") }); err == nil || got != "" {
		t.Fatalf("get = %q, %v, want the error", got, err)
	}
	got, err := summary.get(now, func() (psiphon.TunnelStatus, error) { return psiphon.TunnelStatus{Connected: true}, nil })
	if err != nil || got != "tunnel up" {
		t.Fatalf("get after a failed read = %q, %v, want it read again at once, not held for the wait", got, err)
	}
}
