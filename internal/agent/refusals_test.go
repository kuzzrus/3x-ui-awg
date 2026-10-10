package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRefusalLogHoldsBackARepeatForAMinute(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	log := refusalLog{
		now:   func() time.Time { return clock },
		warnf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)

	log.note(req, "wrong secret")
	log.note(req, "wrong secret")
	log.note(req, "no secret")
	clock = clock.Add(refusalLogEvery - time.Second)
	log.note(req, "wrong secret")
	if len(lines) != 2 {
		t.Fatalf("logged %d lines inside the minute, want one per reason:\n%s", len(lines), strings.Join(lines, "\n"))
	}

	clock = clock.Add(time.Second)
	log.note(req, "wrong secret")
	if len(lines) != 3 || !strings.HasSuffix(lines[2], "wrong secret (2 more like it since the last line)") {
		t.Fatalf("lines after the minute = %q, want a new one that counts the held ones", lines)
	}
	log.note(req, "no secret")
	if len(lines) != 4 || strings.Contains(lines[3], "more like it") {
		t.Fatalf("a reason with nothing held back = %q, want a plain line", lines[3:])
	}
}

func TestRefusalLogQuotesWhatTheClientSent(t *testing.T) {
	var lines []string
	log := refusalLog{warnf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }}
	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	req.URL.Path = "/v1/\nagent: refused PUT \"/v1/config\": forged"

	log.note(req, "unknown path")
	if len(lines) != 1 || strings.Contains(lines[0], "\n") {
		t.Fatalf("lines = %q, want one line with the path escaped", lines)
	}
}

// The port is open to the world and a failed handshake needs no secret, so what the http server
// says about one must not be able to fill the log that GET /v1/logs serves.
func TestServerLogHoldsBackAFloodOfComplaints(t *testing.T) {
	var lines []string
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	log := serverLog{
		now:    func() time.Time { return clock },
		debugf: func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) },
	}

	for i := 0; i < 10000; i++ {
		if _, err := fmt.Fprintf(&log, "http: TLS handshake error from 203.0.113.%d: no server name\n", i%250); err != nil {
			t.Fatal(err)
		}
	}
	if len(lines) != 1 || strings.Contains(lines[0], "\n") {
		t.Fatalf("%d lines for a flood inside the minute, want the first one on a line of its own: %q", len(lines), lines)
	}

	clock = clock.Add(refusalLogEvery)
	if _, err := fmt.Fprint(&log, "http: TLS handshake error from 198.51.100.1: no server name\n"); err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || !strings.HasSuffix(lines[1], "(9999 more like it since the last line)") {
		t.Fatalf("lines after the minute = %q, want one that counts the held ones", lines)
	}
}
