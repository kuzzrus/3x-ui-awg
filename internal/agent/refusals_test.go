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

// A panic in a handler or a failed accept is nobody's flood, and has to stay in the log whatever
// a scanner has just been held back for.
func TestServerLogKeepsWhatIsNotAHandshakeComplaint(t *testing.T) {
	var debugs, warns []string
	clock := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	log := serverLog{
		now:    func() time.Time { return clock },
		debugf: func(format string, args ...any) { debugs = append(debugs, fmt.Sprintf(format, args...)) },
		warnf:  func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) },
	}

	for range 100 {
		fmt.Fprint(&log, "http: TLS handshake error from 203.0.113.1:4000: EOF\n")
	}
	fmt.Fprint(&log, "http: panic serving 203.0.113.1:4001: boom\ngoroutine 7 [running]:\n")
	fmt.Fprint(&log, "http: Accept error: accept tcp [::]:8443: too many open files; retrying in 5ms\n")
	if len(debugs) != 1 {
		t.Fatalf("handshake lines = %q, want the first one only", debugs)
	}
	if len(warns) != 2 || !strings.Contains(warns[0], "panic serving") || !strings.Contains(warns[0], "goroutine 7") ||
		!strings.Contains(warns[1], "Accept error") {
		t.Fatalf("warnings = %q, want the panic with its trace and the failed accept, each at once", warns)
	}

	clock = clock.Add(refusalLogEvery)
	fmt.Fprint(&log, "http: TLS handshake error from 203.0.113.2:4002: EOF\n")
	if len(debugs) != 2 || !strings.HasSuffix(debugs[1], "(99 more like it since the last line)") {
		t.Fatalf("handshake lines after the minute = %q, want a count of the handshake complaints held back", debugs)
	}
}
