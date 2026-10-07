package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type agentCall struct {
	method, path, query, authorization, contentType string
	body                                            []byte
}

type stubAgent struct {
	node   *model.Node
	secret string
	calls  func() []agentCall
}

// startStubAgent serves the agent's TLS identity (derived server name included) and lets
// the test decide what each request is answered with.
func startStubAgent(t *testing.T, handle func(w http.ResponseWriter, r *http.Request, body []byte)) *stubAgent {
	t.Helper()
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := bundle.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var seen []agentCall
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = append(seen, agentCall{
			method: r.Method, path: r.URL.Path, query: r.URL.RawQuery,
			authorization: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), body: body,
		})
		mu.Unlock()
		handle(w, r, body)
	}))
	srv.TLS = tlsConfig
	srv.StartTLS()
	t.Cleanup(srv.Close)

	return &stubAgent{
		node: &model.Node{
			Id: 1, Name: "agent-1", Kind: model.NodeKindAgent, Address: "127.0.0.1",
			Port:     srv.Listener.Addr().(*net.TCPAddr).Port,
			ApiToken: bundle.Secret, PinnedCertSha256: fingerprint, AllowPrivateAddress: true,
		},
		secret: bundle.Secret,
		calls: func() []agentCall {
			mu.Lock()
			defer mu.Unlock()
			return append([]agentCall(nil), seen...)
		},
	}
}

func writeJSONReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func TestAgentClientSpeaksTheProtocol(t *testing.T) {
	config := []byte("{\n  \"inbounds\": [],\n  \"note\": \"a&b\"\n}")
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		switch r.Method + " " + r.URL.Path {
		case "GET " + agentproto.PathStatus:
			writeJSONReply(w, http.StatusOK, agentproto.Status{Guid: "g-1", XrayState: agentproto.XrayStateRunning, XrayVersion: "26.9.9"})
		case "GET " + agentproto.PathStats:
			writeJSONReply(w, http.StatusOK, agentproto.Stats{
				XrayStartedAt: 1700000000000,
				Inbounds:      map[string]agentproto.Counter{"in-1": {Up: 1, Down: 2}},
				Users:         map[string]agentproto.Counter{"a@x": {Up: 3, Down: 4}},
				Online:        []string{"a@x"},
			})
		case "PUT " + agentproto.PathConfig:
			restart, _ := strconv.ParseBool(r.URL.Query().Get(agentproto.QueryRestartOnUserRemoval))
			writeJSONReply(w, http.StatusOK, agentproto.ConfigResponse{
				Revision: agentproto.RevisionOf(body, restart), Applied: agentproto.AppliedHot, XrayState: agentproto.XrayStateRunning,
			})
		case "POST " + agentproto.PathRestart:
			writeJSONReply(w, http.StatusOK, agentproto.ConfigResponse{Revision: "r", Applied: agentproto.AppliedRestart})
		default:
			http.NotFound(w, r)
		}
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatalf("NewAgentClient: %v", err)
	}
	ctx := context.Background()

	status, err := client.Status(ctx)
	if err != nil || status.Guid != "g-1" || status.XrayVersion != "26.9.9" {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	stats, err := client.Stats(ctx)
	if err != nil || stats.XrayStartedAt != 1700000000000 || stats.Users["a@x"] != (agentproto.Counter{Up: 3, Down: 4}) || stats.Online[0] != "a@x" {
		t.Fatalf("Stats = %+v, %v", stats, err)
	}
	pushed, err := client.PushConfig(ctx, config, true)
	if err != nil || pushed.Applied != agentproto.AppliedHot || pushed.Revision != agentproto.RevisionOf(config, true) {
		t.Fatalf("PushConfig = %+v, %v", pushed, err)
	}
	if restarted, err := client.Restart(ctx); err != nil || restarted.Applied != agentproto.AppliedRestart {
		t.Fatalf("Restart = %+v, %v", restarted, err)
	}

	calls := agent.calls()
	if len(calls) != 4 {
		t.Fatalf("agent saw %d requests, want 4: %+v", len(calls), calls)
	}
	for _, call := range calls {
		if call.authorization != "Bearer "+agent.secret {
			t.Fatalf("%s %s carried authorization %q", call.method, call.path, call.authorization)
		}
	}
	push := calls[2]
	if push.method != http.MethodPut || push.path != agentproto.PathConfig || push.query != agentproto.QueryRestartOnUserRemoval+"=true" ||
		push.contentType != "application/json" || string(push.body) != string(config) {
		t.Fatalf("config push reached the agent as %+v, want the raw body with the policy in the query", push)
	}
}

func TestAgentClientReportsWhatTheAgentSaid(t *testing.T) {
	tests := []struct {
		name        string
		reply       func(w http.ResponseWriter)
		call        func(c *AgentClient) error
		wantStatus  int
		wantMessage string
		wantRefused bool
	}{
		{
			"the agent refuses a config",
			func(w http.ResponseWriter) {
				writeJSONReply(w, http.StatusUnprocessableEntity, agentproto.ErrorBody{Error: "the core rejected the config: boom"})
			},
			func(c *AgentClient) error {
				_, err := c.PushConfig(context.Background(), []byte("{}"), false)
				return err
			},
			http.StatusUnprocessableEntity, "the core rejected the config: boom", true,
		},
		{
			"the agent has nothing to restart",
			func(w http.ResponseWriter) {
				writeJSONReply(w, http.StatusConflict, agentproto.ErrorBody{Error: "no config has been applied yet"})
			},
			func(c *AgentClient) error { _, err := c.Restart(context.Background()); return err },
			http.StatusConflict, "no config has been applied yet", false,
		},
		{
			"a 404 means the secret was not accepted",
			func(w http.ResponseWriter) { http.NotFound(w, nil) },
			func(c *AgentClient) error { _, err := c.Status(context.Background()); return err },
			http.StatusNotFound, "did not accept the secret", false,
		},
		{
			"a plain-text failure is quoted",
			func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusBadGateway)
				_, _ = w.Write([]byte("upstream\x1b[31m down\n"))
			},
			func(c *AgentClient) error { _, err := c.Stats(context.Background()); return err },
			http.StatusBadGateway, `"upstream\x1b[31m down"`, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) { tt.reply(w) })
			client, err := NewAgentClient(agent.node, "")
			if err != nil {
				t.Fatal(err)
			}
			err = tt.call(client)
			var got *AgentError
			if !errors.As(err, &got) {
				t.Fatalf("error = %v, want an *AgentError", err)
			}
			if got.Status != tt.wantStatus || !strings.Contains(got.Message, tt.wantMessage) || got.Refused() != tt.wantRefused {
				t.Fatalf("AgentError = %+v (refused %v), want status %d containing %q, refused %v",
					got, got.Refused(), tt.wantStatus, tt.wantMessage, tt.wantRefused)
			}
		})
	}
}

func TestAgentClientRefusesAnAnswerItCannotTrust(t *testing.T) {
	tests := []struct {
		name  string
		reply func(w http.ResponseWriter)
		call  func(c *AgentClient) error
		want  string
	}{
		{
			"a revision the agent computed over other bytes",
			func(w http.ResponseWriter) {
				writeJSONReply(w, http.StatusOK, agentproto.ConfigResponse{Revision: "deadbeef", Applied: agentproto.AppliedNoop})
			},
			func(c *AgentClient) error {
				_, err := c.PushConfig(context.Background(), []byte("{}"), false)
				return err
			},
			"agent applied revision deadbeef",
		},
		{
			"a success that is not JSON",
			func(w http.ResponseWriter) { _, _ = w.Write([]byte("<html>")) },
			func(c *AgentClient) error { _, err := c.Status(context.Background()); return err },
			"decode " + agentproto.PathStatus + " response",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) { tt.reply(w) })
			client, err := NewAgentClient(agent.node, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := tt.call(client); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one containing %q", err, tt.want)
			}
		})
	}
}

// A heartbeat reads a status on a schedule from every agent, so its answer gets a tighter
// cap than the stats, which hold a counter for every inbound and user.
func TestAgentClientCapsStatusTighterThanStats(t *testing.T) {
	body := `{"hostname":"` + strings.Repeat("a", 2<<20) + `","online":[]}`
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds size limit") {
		t.Fatalf("a 2 MiB status = %v, want it refused as too large", err)
	}
	if _, err := client.Stats(context.Background()); err != nil {
		t.Fatalf("a 2 MiB stats answer = %v, want it accepted", err)
	}
}

// The node's address is an IP here, for which crypto/tls sends no server name on its own:
// the gate only opens because the client names the one derived from the secret.
func TestAgentClientTrustsOnlyThePinnedAgent(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.Status{Guid: "g"})
	})

	t.Run("the right secret and pin", func(t *testing.T) {
		client, err := NewAgentClient(agent.node, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Status(context.Background()); err != nil {
			t.Fatalf("Status: %v", err)
		}
	})
	t.Run("another certificate than the pinned one", func(t *testing.T) {
		wrong := *agent.node
		wrong.PinnedCertSha256 = strings.Repeat("ab", 32)
		client, err := NewAgentClient(&wrong, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "does not match the pinned SHA-256") {
			t.Fatalf("error = %v, want a pin mismatch", err)
		}
	})
	t.Run("another agent's secret", func(t *testing.T) {
		other, err := agentproto.NewSecret()
		if err != nil {
			t.Fatal(err)
		}
		wrong := *agent.node
		wrong.ApiToken = other
		client, err := NewAgentClient(&wrong, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.Status(context.Background()); err == nil {
			t.Fatal("the handshake succeeded for the server name of another agent")
		}
	})
}

func TestNewAgentClientRefuses(t *testing.T) {
	base := model.Node{
		Id: 1, Name: "n", Kind: model.NodeKindAgent, Address: "127.0.0.1", Port: 8443,
		ApiToken: "secret", PinnedCertSha256: hex.EncodeToString(make([]byte, 32)),
	}
	panel, noSecret, badPort, badPin := base, base, base, base
	panel.Kind = model.NodeKindPanel
	noSecret.ApiToken = ""
	badPort.Port = 0
	badPin.PinnedCertSha256 = "not a pin"

	tests := []struct {
		name string
		node model.Node
		want string
	}{
		{"a panel node", panel, "is not an agent"},
		{"no secret", noSecret, "no secret configured"},
		{"no port", badPort, "invalid agent port"},
		{"a pin that is no hash", badPin, "certificate pin"},
		{"a secret that is not a secret", base, "agent secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if client, err := NewAgentClient(&tt.node, ""); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewAgentClient = %v, %v; want an error containing %q", client, err, tt.want)
			}
		})
	}
}

func TestManagerAgentFor(t *testing.T) {
	m := NewManager(LocalDeps{})
	node := &model.Node{Id: 3, Name: "agent-3", Kind: model.NodeKindAgent, Address: "203.0.113.7", Port: 8443, ApiToken: "s1", PinnedCertSha256: "p1"}

	first, err := m.AgentFor(node)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := m.AgentFor(node); err != nil || again != first {
		t.Fatalf("a second call = %v, %v; want the cached runtime", again, err)
	}
	if first.Name() != "node:agent-3" {
		t.Fatalf("Name = %q", first.Name())
	}

	for name, change := range map[string]func(n *model.Node){
		"a new secret":  func(n *model.Node) { n.ApiToken = "s2" },
		"a new pin":     func(n *model.Node) { n.PinnedCertSha256 = "p2" },
		"a new address": func(n *model.Node) { n.Address = "203.0.113.8" },
	} {
		changed := *node
		change(&changed)
		rebuilt, err := m.AgentFor(&changed)
		if err != nil || rebuilt == first {
			t.Fatalf("%s: AgentFor = %v, %v; want a rebuilt runtime", name, rebuilt, err)
		}
		first = rebuilt
		node = &changed
	}

	panel := *node
	panel.Kind = model.NodeKindPanel
	if rt, err := m.AgentFor(&panel); err == nil {
		t.Fatalf("AgentFor accepted a panel node: %v", rt)
	}
	if _, err := m.AgentFor(nil); err == nil {
		t.Fatal("AgentFor accepted a nil node")
	}

	m.InvalidateNode(3)
	if rebuilt, err := m.AgentFor(node); err != nil || rebuilt == first {
		t.Fatalf("after InvalidateNode = %v, %v; want a new runtime", rebuilt, err)
	}
}

func TestManagerRemoteForRefusesAnAgent(t *testing.T) {
	m := NewManager(LocalDeps{})
	agent := &model.Node{Id: 4, Name: "agent-4", Kind: model.NodeKindAgent, Address: "203.0.113.7", Port: 8443}
	if rt, err := m.RemoteFor(agent); err == nil || !strings.Contains(err.Error(), "is an agent, not a panel") {
		t.Fatalf("RemoteFor(agent) = %v, %v; want a refusal", rt, err)
	}
	if rt, err := m.RemoteFor(&model.Node{Id: 5, Name: "panel-5", Address: "203.0.113.8", Port: 2053}); err != nil || rt == nil {
		t.Fatalf("RemoteFor(panel with no kind set) = %v, %v; an old row without a kind is a panel", rt, err)
	}
}

func TestAgentRuntimeDoesNothingPerInboundAndRestartsThroughTheAgent(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.ConfigResponse{Applied: agentproto.AppliedRestart})
	})
	rt := NewAgentRuntime(agent.node, nil)
	ctx := context.Background()
	ib := &model.Inbound{}

	for name, err := range map[string]error{
		"AddInbound":          rt.AddInbound(ctx, ib),
		"DelInbound":          rt.DelInbound(ctx, ib),
		"UpdateInbound":       rt.UpdateInbound(ctx, ib, ib),
		"AddUser":             rt.AddUser(ctx, ib, nil),
		"RemoveUser":          rt.RemoveUser(ctx, ib, "a"),
		"UpdateUser":          rt.UpdateUser(ctx, ib, "a", model.Client{}),
		"DeleteUser":          rt.DeleteUser(ctx, ib, "a"),
		"AddClient":           rt.AddClient(ctx, ib, model.Client{}),
		"DeleteClient":        rt.DeleteClient(ctx, "a"),
		"ResetClientTraffic":  rt.ResetClientTraffic(ctx, ib, "a"),
		"ResetInboundTraffic": rt.ResetInboundTraffic(ctx, ib),
		"ResetAllTraffics":    rt.ResetAllTraffics(ctx),
	} {
		if err != nil {
			t.Fatalf("%s = %v, want nil", name, err)
		}
	}
	if calls := agent.calls(); len(calls) != 0 {
		t.Fatalf("per-inbound calls reached the agent: %+v", calls)
	}

	if err := rt.RestartXray(ctx); err != nil {
		t.Fatalf("RestartXray: %v", err)
	}
	if calls := agent.calls(); len(calls) != 1 || calls[0].method != http.MethodPost || calls[0].path != agentproto.PathRestart {
		t.Fatalf("RestartXray reached the agent as %+v, want one POST %s", calls, agentproto.PathRestart)
	}
}
