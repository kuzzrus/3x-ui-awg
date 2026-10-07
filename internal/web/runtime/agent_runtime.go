package runtime

import (
	"context"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// AgentRuntime is the runtime of an agent node: the master pushes the node's whole config,
// so per-inbound and per-client calls have nothing to do (the mutation marked it dirty).
type AgentRuntime struct {
	node *model.Node

	mu             sync.Mutex
	client         *AgentClient
	egressResolver NodeEgressResolver
}

func NewAgentRuntime(n *model.Node, r NodeEgressResolver) *AgentRuntime {
	return &AgentRuntime{node: n, egressResolver: r}
}

func (r *AgentRuntime) Name() string { return "node:" + r.node.Name }

// Client is built on first use and kept: a node change drops the whole runtime, so the
// client always matches the node it was built from.
func (r *AgentRuntime) Client() (*AgentClient, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.client != nil {
		return r.client, nil
	}
	proxyURL := ""
	if r.node.OutboundTag != "" && r.egressResolver != nil {
		proxyURL = r.egressResolver.NodeEgressProxyURL(r.node.Id)
	}
	client, err := NewAgentClient(r.node, proxyURL)
	if err != nil {
		return nil, err
	}
	r.client = client
	return client, nil
}

func (r *AgentRuntime) AddInbound(context.Context, *model.Inbound) error { return nil }

func (r *AgentRuntime) DelInbound(context.Context, *model.Inbound) error { return nil }

func (r *AgentRuntime) UpdateInbound(context.Context, *model.Inbound, *model.Inbound) error {
	return nil
}

func (r *AgentRuntime) AddUser(context.Context, *model.Inbound, map[string]any) error { return nil }

func (r *AgentRuntime) RemoveUser(context.Context, *model.Inbound, string) error { return nil }

func (r *AgentRuntime) UpdateUser(context.Context, *model.Inbound, string, model.Client) error {
	return nil
}

func (r *AgentRuntime) DeleteUser(context.Context, *model.Inbound, string) error { return nil }

func (r *AgentRuntime) AddClient(context.Context, *model.Inbound, model.Client) error { return nil }

func (r *AgentRuntime) DeleteClient(context.Context, string) error { return nil }

func (r *AgentRuntime) RestartXray(ctx context.Context) error {
	client, err := r.Client()
	if err != nil {
		return err
	}
	_, err = client.Restart(ctx)
	return err
}

// The agent keeps no counters to reset: usage is accounted on the master against its
// stored baselines, so a reset there is a database operation.
func (r *AgentRuntime) ResetClientTraffic(context.Context, *model.Inbound, string) error {
	return nil
}

func (r *AgentRuntime) ResetInboundTraffic(context.Context, *model.Inbound) error { return nil }

func (r *AgentRuntime) ResetAllTraffics(context.Context) error { return nil }

var _ Runtime = (*AgentRuntime)(nil)
