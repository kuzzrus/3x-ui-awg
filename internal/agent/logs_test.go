//go:build !windows

package agent

import (
	"net/http"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

func (s *serverFixture) logs(query string) reply {
	s.t.Helper()
	return s.authed(http.MethodGet, agentproto.PathLogs+query, nil)
}

func TestServerServesTheNewestLogLinesFirst(t *testing.T) {
	s := newServerFixture(t)
	tag := "logs-order-test"
	for i := 1; i <= 3; i++ {
		logger.Infof("%s line %d", tag, i)
	}

	got := decode[agentproto.Logs](t, s.logs("?"+agentproto.QueryLogCount+"=3"), http.StatusOK)
	if len(got.Lines) != 3 {
		t.Fatalf("lines = %v, want the three newest", got.Lines)
	}
	for i, want := range []string{"line 3", "line 2", "line 1"} {
		if !strings.Contains(got.Lines[i], tag+" "+want) {
			t.Fatalf("line %d = %q, want it to end with %q: newest first", i, got.Lines[i], want)
		}
	}
}

func TestServerFiltersTheLogByLevel(t *testing.T) {
	s := newServerFixture(t)
	logger.Info("level-test info line")
	logger.Error("level-test error line")

	errors := decode[agentproto.Logs](t, s.logs("?"+agentproto.QueryLogLevel+"=error&"+agentproto.QueryLogCount+"=1000"), http.StatusOK)
	joined := strings.Join(errors.Lines, "\n")
	if !strings.Contains(joined, "level-test error line") || strings.Contains(joined, "level-test info line") {
		t.Fatalf("error level served %q, want the error line and not the info one", joined)
	}
	all := decode[agentproto.Logs](t, s.logs("?"+agentproto.QueryLogLevel+"=debug&"+agentproto.QueryLogCount+"=1000"), http.StatusOK)
	if joined := strings.Join(all.Lines, "\n"); !strings.Contains(joined, "level-test info line") {
		t.Fatalf("debug level lacks the info line: %q", joined)
	}
	// The panel's own log dialog calls the error level err.
	if r := s.logs("?" + agentproto.QueryLogLevel + "=err"); r.status != http.StatusOK {
		t.Fatalf("level err answered %d %s, want the error level", r.status, r.body)
	}
}

func TestServerCapsHowManyLogLinesOneRequestReturns(t *testing.T) {
	s := newServerFixture(t)
	for i := 0; i < agentproto.MaxLogLines+5; i++ {
		logger.Infof("cap-test line %d", i)
	}

	got := decode[agentproto.Logs](t, s.logs("?"+agentproto.QueryLogCount+"=100000"), http.StatusOK)
	if len(got.Lines) != agentproto.MaxLogLines {
		t.Fatalf("%d lines, want the cap of %d", len(got.Lines), agentproto.MaxLogLines)
	}
	if def := decode[agentproto.Logs](t, s.logs(""), http.StatusOK); len(def.Lines) != defaultLogLines {
		t.Fatalf("%d lines without a count, want the default %d", len(def.Lines), defaultLogLines)
	}
}

func TestServerRefusesALogRequestItCannotRead(t *testing.T) {
	s := newServerFixture(t)

	for _, query := range []string{"?count=0", "?count=-3", "?count=many", "?level=loud"} {
		if r := s.logs(query); r.status != http.StatusBadRequest {
			t.Errorf("%s answered %d %s, want 400", query, r.status, r.body)
		}
	}
}

func TestServerKeepsTheLogsBehindTheSecretAndTheMethod(t *testing.T) {
	s := newServerFixture(t)

	for _, tt := range []struct{ method, authorization string }{
		{http.MethodGet, ""},
		{http.MethodGet, "Bearer wrong"},
		{http.MethodPost, "Bearer " + s.bundle.Secret},
		{http.MethodPut, "Bearer " + s.bundle.Secret},
	} {
		if r := s.do(tt.method, agentproto.PathLogs, nil, tt.authorization); r.status != http.StatusNotFound {
			t.Errorf("%s with %q: status = %d, want the bare 404", tt.method, tt.authorization, r.status)
		}
	}
}
