package runtime

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// ErrAgentNotConnected is what a call to an agent in pull mode fails with while that agent holds no connection.
var ErrAgentNotConnected = errors.New("the agent has not connected to the panel")

// tunnelSetupTimeout bounds the HTTP/2 handshake with an agent that has just connected.
const tunnelSetupTimeout = 10 * time.Second

// AgentTunnels holds the connection that each agent in pull mode opened to the panel, over which the panel's
// client sends its requests as HTTP/2, with the roles turned: the panel is the client of an agent that dialled it.
type AgentTunnels struct {
	mu    sync.Mutex
	conns map[int]*agentTunnel
}

type agentTunnel struct {
	conn  *agentproto.Conn
	cc    *http.ClientConn
	since time.Time
}

var agentTunnels = NewAgentTunnels()

// GetAgentTunnels is the registry that the panel's connect endpoint fills and its agent clients read.
func GetAgentTunnels() *AgentTunnels { return agentTunnels }

// NewAgentTunnels is a registry of its own, which is what a test wants.
func NewAgentTunnels() *AgentTunnels { return &AgentTunnels{conns: map[int]*agentTunnel{}} }

// Attach makes ws the connection of the agent of node, and closes the one it replaces: an agent that dials
// again has lost the old connection, even when the panel has not found out yet.
func (t *AgentTunnels) Attach(node int, ws *websocket.Conn) error {
	tunnel := &agentTunnel{since: time.Now()}
	tunnel.conn = agentproto.NewConn(ws, func() { t.remove(node, tunnel) })
	ctx, cancel := context.WithTimeout(context.Background(), tunnelSetupTimeout)
	defer cancel()
	cc, err := agentproto.NewClientConn(ctx, tunnel.conn)
	if err != nil {
		_ = tunnel.conn.Close()
		return err
	}
	tunnel.cc = cc

	t.mu.Lock()
	old := t.conns[node]
	t.conns[node] = tunnel
	t.mu.Unlock()
	if old != nil {
		_ = old.conn.Close()
	}
	return nil
}

func (t *AgentTunnels) remove(node int, tunnel *agentTunnel) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.conns[node] == tunnel {
		delete(t.conns, node)
	}
}

// Connected reports whether the agent of node holds a connection that can take a request.
func (t *AgentTunnels) Connected(node int) bool { return t.client(node) != nil }

// Drop closes the connection of the agent of node, as when its secret is replaced or the node goes away.
func (t *AgentTunnels) Drop(node int) {
	t.mu.Lock()
	tunnel := t.conns[node]
	delete(t.conns, node)
	t.mu.Unlock()
	if tunnel != nil {
		_ = tunnel.conn.Close()
	}
}

func (t *AgentTunnels) client(node int) *http.ClientConn {
	t.mu.Lock()
	tunnel := t.conns[node]
	t.mu.Unlock()
	if tunnel == nil || tunnel.cc.Err() != nil {
		return nil
	}
	return tunnel.cc
}

// RoundTripper sends each request over the connection the agent of node holds at that moment, so a client
// built once keeps working across the agent's reconnects.
func (t *AgentTunnels) RoundTripper(node int) http.RoundTripper {
	return tunnelTransport{tunnels: t, node: node}
}

type tunnelTransport struct {
	tunnels *AgentTunnels
	node    int
}

func (tt tunnelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cc := tt.tunnels.client(tt.node)
	if cc == nil {
		return nil, ErrAgentNotConnected
	}
	return cc.RoundTrip(req)
}
