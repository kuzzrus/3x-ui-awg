//go:build !windows

package agent

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

type serverFixture struct {
	*fixture
	bundle *agentproto.Bundle
	client *http.Client
	url    string
	server *Server
}

// newServerFixture serves a Server over real TLS with the bundle's identity, and gives
// back a client built the way the master builds its own.
func newServerFixture(t *testing.T) *serverFixture {
	t.Helper()
	f := newFixture(t)
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(f.core, f.state, bundle.Secret)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := bundle.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(ctx, ln, tlsConfig) }()
	t.Cleanup(func() {
		cancel()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	pin, err := hex.DecodeString(fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	clientTLS, err := agentproto.ClientTLSConfig(bundle.Secret, pin)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: time.Minute}
	t.Cleanup(client.CloseIdleConnections)
	return &serverFixture{fixture: f, bundle: bundle, client: client, url: "https://" + ln.Addr().String(), server: server}
}

type reply struct {
	status      int
	contentType string
	body        string
}

func (s *serverFixture) do(method, path string, body []byte, authorization string) reply {
	s.t.Helper()
	req, err := http.NewRequest(method, s.url+path, bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return reply{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: string(raw)}
}

func (s *serverFixture) authed(method, path string, body []byte) reply {
	s.t.Helper()
	return s.do(method, path, body, "Bearer "+s.bundle.Secret)
}

func decode[T any](t *testing.T, r reply, status int) T {
	t.Helper()
	if r.status != status {
		t.Fatalf("status = %d (%s), want %d", r.status, r.body, status)
	}
	var out T
	if err := json.Unmarshal([]byte(r.body), &out); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
	return out
}

func TestServerAppliesAConfigAndReportsIt(t *testing.T) {
	s := newServerFixture(t)
	body := s.config("a").compact(t)

	resp := decode[agentproto.ConfigResponse](t, s.authed(http.MethodPut, agentproto.PathConfig, body), http.StatusOK)
	want := agentproto.ConfigResponse{
		Revision:  agentproto.RevisionOf(body, false),
		Applied:   agentproto.AppliedRestart,
		XrayState: agentproto.XrayStateRunning,
	}
	if resp != want {
		t.Fatalf("PUT %s = %+v, want %+v", agentproto.PathConfig, resp, want)
	}

	status := decode[agentproto.Status](t, s.authed(http.MethodGet, agentproto.PathStatus, nil), http.StatusOK)
	guid, err := s.state.Guid()
	if err != nil {
		t.Fatal(err)
	}
	hostname, _ := os.Hostname()
	if status.Guid != guid || status.Hostname != hostname || status.AgentVersion != config.GetPanelVersion() {
		t.Fatalf("identity in status = %+v, want guid %s on %s at %s", status, guid, hostname, config.GetPanelVersion())
	}
	if status.ConfigRevision != want.Revision || status.XrayVersion != "9.9.9" || status.XrayState != agentproto.XrayStateRunning || status.XrayError != "" {
		t.Fatalf("core in status = %+v, want revision %s running 9.9.9", status, want.Revision)
	}
	if status.MemPct < 0 || status.MemPct > 100 || status.CpuPct < 0 || status.UptimeSecs == 0 {
		t.Fatalf("host figures in status = %+v are out of range", status)
	}
}

func TestServerTakesTheRestartPolicyFromTheQuery(t *testing.T) {
	s := newServerFixture(t)
	body := s.config("a").compact(t)

	path := agentproto.PathConfig + "?" + agentproto.QueryRestartOnUserRemoval + "=true"
	resp := decode[agentproto.ConfigResponse](t, s.authed(http.MethodPut, path, body), http.StatusOK)
	if want := agentproto.RevisionOf(body, true); resp.Revision != want {
		t.Fatalf("revision = %q, want %q for a push that asks to restart", resp.Revision, want)
	}
	if saved, err := s.state.LastGood(); err != nil || saved == nil || !saved.RestartOnUserRemoval {
		t.Fatalf("last good = %v, %v; want the policy stored", saved, err)
	}
}

func TestServerRefusesAConfigWith422(t *testing.T) {
	s := newServerFixture(t)
	s.mustApply(s.config("a").compact(t))

	refusal := decode[agentproto.ErrorBody](t, s.authed(http.MethodPut, agentproto.PathConfig, []byte("{not json")), http.StatusUnprocessableEntity)
	if !strings.Contains(refusal.Error, "config is not a valid Xray config") {
		t.Fatalf("error = %q", refusal.Error)
	}
	if got := s.starts(); got != 1 {
		t.Fatalf("core started %d times, want the first run only", got)
	}
}

func TestServerRestart(t *testing.T) {
	s := newServerFixture(t)
	if refusal := decode[agentproto.ErrorBody](t, s.authed(http.MethodPost, agentproto.PathRestart, nil), http.StatusConflict); refusal.Error != ErrNoConfig.Error() {
		t.Fatalf("error = %q, want %q", refusal.Error, ErrNoConfig)
	}

	body := s.config("a").compact(t)
	s.mustApply(body)
	resp := decode[agentproto.ConfigResponse](t, s.authed(http.MethodPost, agentproto.PathRestart, nil), http.StatusOK)
	want := agentproto.ConfigResponse{
		Revision:  agentproto.RevisionOf(body, false),
		Applied:   agentproto.AppliedRestart,
		XrayState: agentproto.XrayStateRunning,
	}
	if resp != want || s.starts() != 2 {
		t.Fatalf("restart = %+v after %d starts, want %+v after 2", resp, s.starts(), want)
	}
}

// The config is the one already running, so a core that will not come back is not a refused push.
func TestServerRestartOfACoreThatWillNotComeBackIsNot422(t *testing.T) {
	s := newServerFixture(t)
	if err := s.state.SaveLastGood(s.config(markerFailStart).compact(t), false); err != nil {
		t.Fatal(err)
	}
	if err := s.core.Boot(context.Background()); err == nil {
		t.Fatal("Boot started a config the core cannot run")
	}

	failure := decode[agentproto.ErrorBody](t, s.authed(http.MethodPost, agentproto.PathRestart, nil), http.StatusInternalServerError)
	if !strings.Contains(failure.Error, "xray exited right after it started") {
		t.Fatalf("error = %q, want why the core did not come back", failure.Error)
	}
}

func TestServerStatsOfACoreThatIsNotRunning(t *testing.T) {
	s := newServerFixture(t)
	stats := decode[agentproto.Stats](t, s.authed(http.MethodGet, agentproto.PathStats, nil), http.StatusOK)
	if stats.XrayStartedAt != 0 || len(stats.Inbounds) != 0 || len(stats.Users) != 0 || len(stats.Online) != 0 {
		t.Fatalf("stats = %+v, want nothing", stats)
	}
}

func TestServerAnswersEverythingElseWithTheSameBare404(t *testing.T) {
	s := newServerFixture(t)
	body := s.config("a").compact(t)
	wrong := "Bearer " + strings.Repeat("A", 43)

	tests := []struct {
		name          string
		method, path  string
		authorization string
	}{
		{"unknown path", http.MethodGet, "/v1/nothing", "Bearer " + s.bundle.Secret},
		{"root", http.MethodGet, "/", "Bearer " + s.bundle.Secret},
		{"trailing slash", http.MethodGet, agentproto.PathStatus + "/", "Bearer " + s.bundle.Secret},
		{"wrong method on config", http.MethodGet, agentproto.PathConfig, "Bearer " + s.bundle.Secret},
		{"wrong method on status", http.MethodPost, agentproto.PathStatus, "Bearer " + s.bundle.Secret},
		{"no secret on status", http.MethodGet, agentproto.PathStatus, ""},
		{"wrong secret on status", http.MethodGet, agentproto.PathStatus, wrong},
		{"wrong secret on config", http.MethodPut, agentproto.PathConfig, wrong},
		{"no secret on restart", http.MethodPost, agentproto.PathRestart, ""},
		{"secret without its scheme", http.MethodGet, agentproto.PathStats, s.bundle.Secret},
	}
	want := reply{status: http.StatusNotFound, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.do(tt.method, tt.path, body, tt.authorization); got != want {
				t.Fatalf("%s %s = %+v, want %+v", tt.method, tt.path, got, want)
			}
		})
	}
	if got := s.starts(); got != 0 {
		t.Fatalf("a request without the secret started the core %d times", got)
	}
}

// The response never says why, so the log is the only place an operator can find out.
func TestServerLogsWhyItRefusedARequest(t *testing.T) {
	s := newServerFixture(t)
	var lines []string
	s.server.refusals.warnf = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
	wrong := "Bearer " + strings.Repeat("A", 43)
	call := func(method, path, authorization string) {
		req := httptest.NewRequest(method, path, nil)
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		s.server.ServeHTTP(httptest.NewRecorder(), req)
	}

	call(http.MethodGet, "/v1/nothing", "Bearer "+s.bundle.Secret)
	call(http.MethodGet, agentproto.PathConfig, "Bearer "+s.bundle.Secret)
	call(http.MethodGet, agentproto.PathStatus, "")
	call(http.MethodGet, agentproto.PathStatus, wrong)
	call(http.MethodGet, agentproto.PathStatus, "Bearer "+s.bundle.Secret)

	want := []string{
		`agent: refused GET "/v1/nothing" from 192.0.2.1:1234: unknown path`,
		`agent: refused GET "/v1/config" from 192.0.2.1:1234: wrong method`,
		`agent: refused GET "/v1/status" from 192.0.2.1:1234: no secret`,
		`agent: refused GET "/v1/status" from 192.0.2.1:1234: wrong secret`,
	}
	if len(lines) != len(want) {
		t.Fatalf("logged %d lines, want %d (the repeat of a reason within the minute is held back, the valid call is not a refusal):\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, lines[i], want[i])
		}
		if strings.Contains(lines[i], s.bundle.Secret) || strings.Contains(lines[i], wrong) {
			t.Fatalf("line %d carries a secret: %q", i, lines[i])
		}
	}
}

// Through the handler directly: a 32 MiB body over a real connection makes the client
// race the server's early reply.
func TestServerRefusesWhatIsNotAWellFormedPush(t *testing.T) {
	s := newServerFixture(t)
	call := func(path string, body io.Reader) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, path, body)
		req.Header.Set("Authorization", "Bearer "+s.bundle.Secret)
		rec := httptest.NewRecorder()
		s.server.ServeHTTP(rec, req)
		return rec
	}

	huge := call(agentproto.PathConfig, strings.NewReader(strings.Repeat(" ", agentproto.MaxConfigBytes+1)))
	if huge.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body past the cap = %d, want 413", huge.Code)
	}
	badFlag := call(agentproto.PathConfig+"?"+agentproto.QueryRestartOnUserRemoval+"=maybe", strings.NewReader("{}"))
	if badFlag.Code != http.StatusBadRequest || !strings.Contains(badFlag.Body.String(), "must be true or false") {
		t.Fatalf("a flag that is no bool = %d %s, want 400", badFlag.Code, badFlag.Body.String())
	}
	if got := s.starts(); got != 0 {
		t.Fatalf("refused pushes started the core %d times", got)
	}
}

func TestServerIsUnreachableWithAnotherAgentsSecret(t *testing.T) {
	s := newServerFixture(t)
	other, err := agentproto.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	pin := bytes.Repeat([]byte{1}, 32)
	clientTLS, err := agentproto.ClientTLSConfig(other, pin)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: clientTLS}, Timeout: 10 * time.Second}
	defer client.CloseIdleConnections()
	if resp, err := client.Get(s.url + agentproto.PathStatus); err == nil {
		_ = resp.Body.Close()
		t.Fatal("the handshake succeeded for a client that derived another server name")
	}
}
