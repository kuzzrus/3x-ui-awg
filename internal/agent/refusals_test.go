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
