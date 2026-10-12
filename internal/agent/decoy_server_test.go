//go:build !windows

package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func (s *serverFixture) putDecoy(body string) reply {
	s.t.Helper()
	return s.authed(http.MethodPut, agentproto.PathDecoy, []byte(body))
}

const servableDecoy = `{"mode":"template","template":"parked","domain":"node.example"}`

func TestServerSetsTheDecoyAndReportsIt(t *testing.T) {
	s := newServerFixture(t)

	got := decode[agentproto.Decoy](t, s.putDecoy(servableDecoy), http.StatusOK)
	want := agentproto.DecoySpec{
		Mode: agentproto.DecoyTemplate, Template: "parked", Domain: "node.example",
		Cert: agentproto.DecoyCertAuto, Port: agentproto.DefaultDecoyPort,
	}
	if got.Spec != want || got.Revision != agentproto.DecoyRevision(want) || got.State.State != agentproto.DecoyStateStarting {
		t.Fatalf("answer = %+v, want the decoy as normalized, not started because the host has no loop in this test", got)
	}

	status := decode[agentproto.Status](t, s.authed(http.MethodGet, agentproto.PathStatus, nil), http.StatusOK)
	if status.Decoy.State != agentproto.DecoyStateStarting {
		t.Fatalf("status decoy = %+v, want the state of the decoy that was set", status.Decoy)
	}
	stats := decode[agentproto.Stats](t, s.authed(http.MethodGet, agentproto.PathStats, nil), http.StatusOK)
	if stats.DecoyRevision != got.Revision {
		t.Fatalf("stats decoyRevision = %q, want %q", stats.DecoyRevision, got.Revision)
	}
	stored, err := os.ReadFile(filepath.Join(s.state.Dir(), decoyFile))
	if err != nil || !strings.Contains(string(stored), `"parked"`) {
		t.Fatalf("stored decoy = %q, %v; want it kept for a restart", stored, err)
	}

	off := decode[agentproto.Decoy](t, s.putDecoy(`{"mode":""}`), http.StatusOK)
	if off.Revision != "" || off.State.State != agentproto.DecoyStateOff {
		t.Fatalf("answer to no decoy = %+v", off)
	}
}

func TestServerRefusesWhatIsNotAServableDecoy(t *testing.T) {
	s := newServerFixture(t)
	s.putDecoy(servableDecoy)
	kept := s.server.decoy.Revision()

	for name, body := range map[string]string{
		"a page it does not have": `{"mode":"template","template":"no-such-page","domain":"node.example"}`,
		"no domain":               `{"mode":"template"}`,
		"a mode it does not know": `{"mode":"upload","domain":"node.example"}`,
		"not json":                `parked`,
		"a wrong type":            `{"mode":7}`,
	} {
		t.Run(name, func(t *testing.T) {
			if got := s.putDecoy(body); got.status != http.StatusBadRequest {
				t.Fatalf("PUT %s = %d %s, want 400", body, got.status, got.body)
			}
		})
	}
	if !strings.Contains(s.putDecoy(`{"mode":"template","template":"no-such-page","domain":"node.example"}`).body, "no-such-page") {
		t.Fatal("the answer does not say which page it does not have")
	}
	if got := s.server.decoy.Revision(); got != kept {
		t.Fatalf("revision = %q after refusals, want %q from before", got, kept)
	}
}

// Through the handler directly: a body past the cap over a real connection makes the client race the early reply.
func TestServerRefusesADecoyBodyPastTheCap(t *testing.T) {
	s := newServerFixture(t)
	body := `{"mode":"proxy","proxyUrl":"https://` + strings.Repeat("a", agentproto.MaxDecoyBytes) + `.example","domain":"node.example"}`
	req := httptest.NewRequest(http.MethodPut, agentproto.PathDecoy, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.bundle.Secret)
	rec := httptest.NewRecorder()
	s.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a body past the cap = %d, want 400", rec.Code)
	}
	if got := s.server.decoy.Revision(); got != "" {
		t.Fatalf("revision = %q, want the decoy untouched", got)
	}
}

func TestServerAnswersTheDecoyPathLikeTheOthers(t *testing.T) {
	s := newServerFixture(t)
	want := reply{status: http.StatusNotFound, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"}
	for name, got := range map[string]reply{
		"wrong method": s.authed(http.MethodGet, agentproto.PathDecoy, nil),
		"no secret":    s.do(http.MethodPut, agentproto.PathDecoy, []byte(servableDecoy), ""),
		"wrong secret": s.do(http.MethodPut, agentproto.PathDecoy, []byte(servableDecoy), "Bearer "+strings.Repeat("A", 43)),
	} {
		if got != want {
			t.Fatalf("%s: %+v, want the bare 404 %+v", name, got, want)
		}
	}
	if got := s.server.decoy.Revision(); got != "" {
		t.Fatalf("revision = %q, want a refused request to change nothing", got)
	}
}
