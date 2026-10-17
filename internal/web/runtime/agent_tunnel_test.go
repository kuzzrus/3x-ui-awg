package runtime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// tunnelMaster serves the panel's end of pull mode on tunnels: every connection that is made to it is attached
// as the connection of node.
func tunnelMaster(t *testing.T, tunnels *AgentTunnels, node int) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{agentproto.ConnectProtocol}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if err := tunnels.Attach(node, ws, nil); err != nil {
			t.Errorf("Attach: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// dialAgent is an agent: it dials the panel and serves handler on the connection until it ends.
func dialAgent(t *testing.T, srv *httptest.Server, handler http.Handler) (ended <-chan struct{}) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{agentproto.ConnectProtocol}}
	ws, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn := agentproto.NewConn(ws, nil)
	t.Cleanup(func() { conn.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		agentproto.ServeConn(context.Background(), conn, &http.Server{Handler: handler})
	}()
	return done
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func askThrough(t *testing.T, rt http.RoundTripper, path string) (int, string, error) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://agent.invalid"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

func answers(text string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(text + " " + r.URL.Path)) })
}

func TestAgentTunnelsSendRequestsOverTheConnectionTheAgentOpened(t *testing.T) {
	tunnels := NewAgentTunnels()
	rt := tunnels.RoundTripper(3)
	if _, _, err := askThrough(t, rt, "/v1/status"); !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("before the agent connected: %v, want ErrAgentNotConnected", err)
	}
	if tunnels.Connected(3) {
		t.Fatal("a node that has not connected is reported connected")
	}

	dialAgent(t, tunnelMaster(t, tunnels, 3), answers("agent"))
	waitFor(t, "the connection to be registered", func() bool { return tunnels.Connected(3) })
	if status, body, err := askThrough(t, rt, "/v1/status"); err != nil || status != http.StatusOK || body != "agent /v1/status" {
		t.Fatalf("answer = %d %q, %v", status, body, err)
	}
	// Another node's agent is not this one.
	if _, _, err := askThrough(t, tunnels.RoundTripper(4), "/v1/status"); !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("a request for another node: %v, want ErrAgentNotConnected", err)
	}
}

// An agent that dials again has lost the old connection, so the new one is the one that serves, and the old one is let go.
// welcomeOf attaches the one connection that is made to it as the agent of node 3, and says how that ended.
func welcomeOf(t *testing.T, tunnels *AgentTunnels, welcome func() bool) (*httptest.Server, <-chan error) {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{agentproto.ConnectProtocol}}
	attached := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			attached <- err
			return
		}
		attached <- tunnels.Attach(3, ws, welcome)
	}))
	t.Cleanup(srv.Close)
	return srv, attached
}

// An agent that dialled with a secret that the node has since replaced must not be registered, or the panel would
// hand the new secret to it.
func TestAgentTunnelsAttachTurnsAwayAnAgentThatIsNoLongerWelcome(t *testing.T) {
	tunnels := NewAgentTunnels()
	srv, attached := welcomeOf(t, tunnels, func() bool { return false })
	ended := dialAgent(t, srv, answers("agent"))
	if err := <-attached; !errors.Is(err, ErrAgentTurnedAway) {
		t.Fatalf("Attach: %v, want ErrAgentTurnedAway", err)
	}
	if tunnels.Connected(3) {
		t.Fatal("an agent that was turned away is registered")
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent that was turned away was not told the connection was closed")
	}
}

// What makes the check hold against a Drop is that it is made where nothing else can change the registry.
func TestAgentTunnelsAttachAsksWhetherTheAgentIsWelcomeWithTheRegistryLocked(t *testing.T) {
	tunnels := NewAgentTunnels()
	srv, attached := welcomeOf(t, tunnels, func() bool {
		if tunnels.mu.TryLock() {
			tunnels.mu.Unlock()
			t.Error("the registry was not locked while the agent was asked about")
		}
		return true
	})
	dialAgent(t, srv, answers("agent"))
	if err := <-attached; err != nil {
		t.Fatalf("Attach: %v", err)
	}
	waitFor(t, "the connection", func() bool { return tunnels.Connected(3) })
}

func TestAgentTunnelsAttachReplacesTheOldConnection(t *testing.T) {
	tunnels := NewAgentTunnels()
	srv := tunnelMaster(t, tunnels, 3)
	firstEnded := dialAgent(t, srv, answers("first"))
	waitFor(t, "the first connection", func() bool { return tunnels.Connected(3) })
	secondEnded := dialAgent(t, srv, answers("second"))

	waitFor(t, "the second connection to answer", func() bool {
		_, body, err := askThrough(t, tunnels.RoundTripper(3), "/x")
		return err == nil && strings.HasPrefix(body, "second")
	})
	select {
	case <-firstEnded:
	case <-time.After(10 * time.Second):
		t.Fatal("the old connection was not closed when the agent dialled again")
	}
	select {
	case <-secondEnded:
		t.Fatal("the new connection ended")
	default:
	}
}

func TestAgentTunnelsDropCutsTheAgentOff(t *testing.T) {
	tunnels := NewAgentTunnels()
	ended := dialAgent(t, tunnelMaster(t, tunnels, 3), answers("agent"))
	waitFor(t, "the connection", func() bool { return tunnels.Connected(3) })

	tunnels.Drop(3)
	if tunnels.Connected(3) {
		t.Fatal("a dropped agent is still reported connected")
	}
	if _, _, err := askThrough(t, tunnels.RoundTripper(3), "/x"); !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("a request after the drop: %v, want ErrAgentNotConnected", err)
	}
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the agent was not told the connection was closed")
	}
	tunnels.Drop(3) // dropping what is not there is no error
}

func TestAgentTunnelsNoticeAnAgentThatWentAway(t *testing.T) {
	tunnels := NewAgentTunnels()
	srv := tunnelMaster(t, tunnels, 3)
	dialer := websocket.Dialer{Subprotocols: []string{agentproto.ConnectProtocol}}
	ws, _, err := dialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := agentproto.NewConn(ws, nil)
	go agentproto.ServeConn(context.Background(), conn, &http.Server{Handler: answers("agent")})
	waitFor(t, "the connection", func() bool { return tunnels.Connected(3) })

	_ = conn.Close()
	waitFor(t, "the panel to notice", func() bool { return !tunnels.Connected(3) })
	if _, _, err := askThrough(t, tunnels.RoundTripper(3), "/x"); !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("a request after the agent went away: %v, want ErrAgentNotConnected", err)
	}
}

// The client of a node in pull mode is built once, and has to keep working across the agent's reconnects.
func TestAgentClientOfAPullNodeSpeaksTheProtocolOverTheTunnel(t *testing.T) {
	const nodeID = 701
	t.Cleanup(func() { GetAgentTunnels().Drop(nodeID) })
	secret, err := agentproto.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	node := &model.Node{Id: nodeID, Name: "pull-1", Kind: model.NodeKindAgent, Pull: true, Address: "203.0.113.5", Port: 443, ApiToken: secret}
	client, err := NewAgentClient(node, "")
	if err != nil {
		t.Fatalf("NewAgentClient: %v", err)
	}

	if _, err := client.Status(context.Background()); !errors.Is(err, ErrAgentNotConnected) {
		t.Fatalf("Status before the agent connected: %v, want ErrAgentNotConnected", err)
	} else if strings.Contains(err.Error(), "agent.invalid") {
		t.Fatalf("the error shows the address that only stands in for the agent: %v", err)
	}

	var sawSecret atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawSecret.Store(agentproto.CheckBearer(r.Header.Get("Authorization"), secret))
		writeJSONReply(w, http.StatusOK, agentproto.Status{Guid: "pulled", XrayState: agentproto.XrayStateRunning})
	})
	srv := tunnelMaster(t, GetAgentTunnels(), nodeID)
	dialAgent(t, srv, handler)
	waitFor(t, "the connection", func() bool { return GetAgentTunnels().Connected(nodeID) })
	status, err := client.Status(context.Background())
	if err != nil || status.Guid != "pulled" {
		t.Fatalf("Status = %+v, %v", status, err)
	}
	if !sawSecret.Load() {
		t.Fatal("the request did not carry the secret of the agent")
	}

	// The agent connects again, and the same client reaches the new connection.
	GetAgentTunnels().Drop(nodeID)
	dialAgent(t, srv, handler)
	waitFor(t, "the second connection", func() bool { return GetAgentTunnels().Connected(nodeID) })
	if status, err = client.Status(context.Background()); err != nil || status.Guid != "pulled" {
		t.Fatalf("Status after a reconnect = %+v, %v", status, err)
	}
}

func TestNewAgentClientOfAPullNodeNeedsNeitherAddressNorPinNorPort(t *testing.T) {
	secret, _ := agentproto.NewSecret()
	node := &model.Node{Id: 702, Name: "pull-2", Kind: model.NodeKindAgent, Pull: true, ApiToken: secret}
	if _, err := NewAgentClient(node, ""); err != nil {
		t.Fatalf("a pull node with nothing to dial was refused: %v", err)
	}
	node.Pull = false
	if _, err := NewAgentClient(node, ""); err == nil {
		t.Fatal("a node the panel dials was accepted without an address, a port and a pin")
	}
}
