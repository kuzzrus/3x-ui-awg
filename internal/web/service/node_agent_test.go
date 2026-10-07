package service

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// startIdleAgent runs a real agent server with no config pushed, which still answers
// status, and returns the node row that points at it.
func startIdleAgent(t *testing.T) (*model.Node, string) {
	t.Helper()
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	state, err := agent.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guid, err := state.Guid()
	if err != nil {
		t.Fatal(err)
	}
	server, err := agent.NewServer(agent.NewCore(state, t.TempDir()), state, bundle.Secret)
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
	return &model.Node{
		Id: 9, Name: "agent-9", Kind: model.NodeKindAgent, Address: "127.0.0.1",
		Port:     ln.Addr().(*net.TCPAddr).Port,
		ApiToken: bundle.Secret, PinnedCertSha256: fingerprint, AllowPrivateAddress: true,
	}, guid
}

func TestProbeAgentFillsTheSameHeartbeatAsAPanel(t *testing.T) {
	node, guid := startIdleAgent(t)

	patch, err := (&NodeService{}).Probe(context.Background(), node)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if patch.LastError != "" || patch.Guid != guid || patch.PanelVersion != config.GetPanelVersion() {
		t.Fatalf("patch = %+v, want the agent's guid %s and version %s", patch, guid, config.GetPanelVersion())
	}
	if patch.XrayState != agentproto.XrayStateStopped || patch.XrayError != "" || patch.XrayVersion != "" {
		t.Fatalf("core fields = %q %q %q, want a stopped core with no version yet", patch.XrayState, patch.XrayError, patch.XrayVersion)
	}
	if patch.MemPct <= 0 || patch.MemPct > 100 || patch.UptimeSecs == 0 || patch.LastHeartbeat == 0 {
		t.Fatalf("host figures in the patch are out of range: %+v", patch)
	}
}

func TestProbeAgentReportsWhyItCannotReachTheAgent(t *testing.T) {
	node, _ := startIdleAgent(t)
	otherSecret, err := agentproto.NewSecret()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		mutate func(n *model.Node)
		want   string
	}{
		{"another certificate than the pinned one", func(n *model.Node) { n.PinnedCertSha256 = strings.Repeat("cd", 32) }, "does not match the pinned SHA-256"},
		{"another agent's secret", func(n *model.Node) { n.ApiToken = otherSecret }, "remote error: tls"},
		{"nothing listening", func(n *model.Node) { n.Port = 1 }, "refused"},
		{"no secret at all", func(n *model.Node) { n.ApiToken = "" }, "no secret configured"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			broken := *node
			tt.mutate(&broken)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			patch, err := (&NodeService{}).Probe(ctx, &broken)
			if err == nil || patch.LastError == "" || !strings.Contains(patch.LastError, tt.want) {
				t.Fatalf("Probe = %+v, %v; want a LastError containing %q", patch, err, tt.want)
			}
		})
	}
}
