//go:build !windows

package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func (s *serverFixture) putSidecars(body string) reply {
	s.t.Helper()
	return s.authed(http.MethodPut, agentproto.PathSidecars, []byte(body))
}

func TestServerSetsTheSidecarsAndReportsThem(t *testing.T) {
	s := newServerFixture(t)
	want := agentproto.SidecarsRevision([]string{agentproto.SidecarTor})

	got := decode[agentproto.Sidecars](t, s.putSidecars(`{"enabled":["tor"]}`), http.StatusOK)
	if got.Revision != want || !slices.Equal(got.Enabled, []string{agentproto.SidecarTor}) ||
		len(got.States) != 1 || got.States[0].Name != agentproto.SidecarTor || got.States[0].State != agentproto.SidecarPending {
		t.Fatalf("answer = %+v, want tor listed and pending, as the host has not looked at it yet", got)
	}

	status := decode[agentproto.Status](t, s.authed(http.MethodGet, agentproto.PathStatus, nil), http.StatusOK)
	if len(status.Sidecars) != 1 || status.Sidecars[0].Name != agentproto.SidecarTor {
		t.Fatalf("status sidecars = %+v, want tor", status.Sidecars)
	}
	stats := decode[agentproto.Stats](t, s.authed(http.MethodGet, agentproto.PathStats, nil), http.StatusOK)
	if stats.SidecarsRevision != want {
		t.Fatalf("stats sidecarsRevision = %q, want %q", stats.SidecarsRevision, want)
	}
	stored, err := os.ReadFile(filepath.Join(s.state.Dir(), sidecarsFile))
	if err != nil || !strings.Contains(string(stored), `"tor"`) {
		t.Fatalf("stored list = %q, %v; want it kept for a restart", stored, err)
	}

	cleared := decode[agentproto.Sidecars](t, s.putSidecars(`{"enabled":[]}`), http.StatusOK)
	if cleared.Revision != "" || len(cleared.Enabled) != 0 {
		t.Fatalf("answer to an empty list = %+v", cleared)
	}
}

// The master reads these as lists, and a null is not one.
func TestServerReportsNoSidecarsAsEmptyLists(t *testing.T) {
	s := newServerFixture(t)
	if body := s.authed(http.MethodGet, agentproto.PathStatus, nil).body; !strings.Contains(body, `"sidecars":[]`) {
		t.Fatalf("status = %s, want an empty list of sidecars", body)
	}
	if body := s.putSidecars(`{"enabled":[]}`).body; !strings.Contains(body, `"enabled":[]`) || !strings.Contains(body, `"states":[]`) {
		t.Fatalf("answer = %s, want empty lists", body)
	}
}

func TestServerRefusesWhatIsNotAListOfKnownSidecars(t *testing.T) {
	s := newServerFixture(t)
	s.putSidecars(`{"enabled":["tor"]}`)

	for name, body := range map[string]string{
		"unknown program": `{"enabled":["tor","no-such-program"]}`,
		"not json":        `tor`,
		"wrong type":      `{"enabled":"tor"}`,
	} {
		t.Run(name, func(t *testing.T) {
			got := s.putSidecars(body)
			if got.status != http.StatusBadRequest {
				t.Fatalf("PUT %s = %d %s, want 400", body, got.status, got.body)
			}
		})
	}
	if unknown := s.putSidecars(`{"enabled":["no-such-program"]}`); !strings.Contains(unknown.body, "no-such-program") {
		t.Fatalf("answer = %s, want the program that is not known named", unknown.body)
	}
	if revision := s.server.sidecars.Revision(); revision != agentproto.SidecarsRevision([]string{agentproto.SidecarTor}) {
		t.Fatalf("revision = %q after refusals, want the list from before", revision)
	}
}

// Through the handler directly: a body past the cap over a real connection makes the client race the early reply.
func TestServerRefusesASidecarsBodyPastTheCap(t *testing.T) {
	s := newServerFixture(t)
	req := httptest.NewRequest(http.MethodPut, agentproto.PathSidecars, strings.NewReader(`{"enabled":["`+strings.Repeat("a", agentproto.MaxSidecarsBytes)+`"]}`))
	req.Header.Set("Authorization", "Bearer "+s.bundle.Secret)
	rec := httptest.NewRecorder()
	s.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body past the cap = %d, want 400", rec.Code)
	}
	if revision := s.server.sidecars.Revision(); revision != "" {
		t.Fatalf("revision = %q, want the list untouched", revision)
	}
}

func TestServerAnswersTheSidecarsPathLikeTheOthers(t *testing.T) {
	s := newServerFixture(t)
	want := reply{status: http.StatusNotFound, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"}
	for name, got := range map[string]reply{
		"wrong method": s.authed(http.MethodGet, agentproto.PathSidecars, nil),
		"no secret":    s.do(http.MethodPut, agentproto.PathSidecars, []byte(`{"enabled":["tor"]}`), ""),
		"wrong secret": s.do(http.MethodPut, agentproto.PathSidecars, []byte(`{"enabled":["tor"]}`), "Bearer "+strings.Repeat("A", 43)),
	} {
		if got != want {
			t.Fatalf("%s: %+v, want the bare 404 %+v", name, got, want)
		}
	}
	if revision := s.server.sidecars.Revision(); revision != "" {
		t.Fatalf("revision = %q, want a refused request to change nothing", revision)
	}
}
