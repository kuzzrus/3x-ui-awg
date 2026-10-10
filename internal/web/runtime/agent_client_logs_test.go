package runtime

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func TestAgentClientReadsTheLog(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusOK, agentproto.Logs{Lines: []string{"newest", "older"}})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := client.Logs(context.Background(), 5, "error")
	if err != nil || len(got.Lines) != 2 || got.Lines[0] != "newest" {
		t.Fatalf("Logs = %+v, %v", got, err)
	}
	calls := agent.calls()
	if len(calls) != 1 || calls[0].method != http.MethodGet || calls[0].path != agentproto.PathLogs ||
		calls[0].query != "count=5&level=error" || calls[0].authorization != "Bearer "+agent.secret {
		t.Fatalf("agent saw %+v, want one authorised GET with the count and the level", calls)
	}
}

func TestAgentClientReportsWhyTheAgentWillNotServeItsLog(t *testing.T) {
	agent := startStubAgent(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSONReply(w, http.StatusBadRequest, agentproto.ErrorBody{Error: "level must be debug, info, notice, warning or error"})
	})
	client, err := NewAgentClient(agent.node, "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Logs(context.Background(), 5, "loud")
	var got *AgentError
	if !errors.As(err, &got) || got.Status != http.StatusBadRequest || !strings.Contains(got.Message, "level must be") {
		t.Fatalf("error = %v, want the agent's own reason as an *AgentError", err)
	}
}
