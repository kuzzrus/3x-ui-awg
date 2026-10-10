package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func TestAgentClientStartsAnUpdateAndReadsHowItWent(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		switch r.Method {
		case http.MethodPost:
			writeJSONReply(w, http.StatusOK, agentproto.UpdateStatus{RunID: "17", State: agentproto.UpdatePending, StartedAt: 100})
		default:
			writeJSONReply(w, http.StatusOK, agentproto.UpdateStatus{RunID: "17", State: agentproto.UpdateFailed, ExitCode: 3, FinishedAt: 160})
		}
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	started, err := client.Update(ctx, true)
	if err != nil || started.RunID != "17" || started.State != agentproto.UpdatePending {
		t.Fatalf("Update = %+v, %v", started, err)
	}
	status, err := client.UpdateStatus(ctx)
	if err != nil || status.State != agentproto.UpdateFailed || status.ExitCode != 3 || status.FinishedAt != 160 {
		t.Fatalf("UpdateStatus = %+v, %v", status, err)
	}

	calls := agent.calls()
	if len(calls) != 2 {
		t.Fatalf("agent saw %d requests, want 2: %+v", len(calls), calls)
	}
	if calls[0].method != http.MethodPost || calls[0].path != agentproto.PathUpdate || calls[0].query != agentproto.QueryUpdateDev+"=true" {
		t.Fatalf("update reached the agent as %+v, want a POST with the dev flag in the query", calls[0])
	}
	if calls[1].method != http.MethodGet || calls[1].path != agentproto.PathUpdate {
		t.Fatalf("status reached the agent as %+v, want a GET", calls[1])
	}
	for _, call := range calls {
		if call.authorization != "Bearer "+agent.secret {
			t.Fatalf("%s %s carried authorization %q", call.method, call.path, call.authorization)
		}
	}
}

func TestAgentClientReportsWhyTheAgentWillNotUpdate(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusConflict, agentproto.ErrorBody{Error: "an update is already running"})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Update(context.Background(), false)
	var got *AgentError
	if !errors.As(err, &got) || got.Status != http.StatusConflict || !strings.Contains(got.Message, "already running") {
		t.Fatalf("error = %v, want the agent's own reason as an *AgentError", err)
	}
}

// An update is started from a request that the panel cuts off after 30 s (web.go), and a bulk update
// answers when its slowest node does, so one agent that does not answer must not outlast that.
func TestAgentUpdateStartGivesUpBeforeThePanelCutsTheRequestOff(t *testing.T) {
	const panelWriteTimeout = 30 * time.Second
	if agentUpdateTimeout > panelWriteTimeout-5*time.Second {
		t.Fatalf("agentUpdateTimeout = %v, want it to leave the panel's %v write deadline room to answer", agentUpdateTimeout, panelWriteTimeout)
	}
}
