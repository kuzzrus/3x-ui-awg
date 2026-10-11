package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const agentTooOldForSidecars = "this agent is too old to run helper programs: update it"

// syncSidecars sends an agent the helper programs its node asks for when the agent reports another list,
// and leaves one that cannot take it alone for a while, the heartbeat saying why.
func (s *AgentSyncService) syncSidecars(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, stats *agentproto.Stats, now time.Time) error {
	if stats == nil {
		return nil
	}
	want, err := agentproto.NormalizeSidecars(n.Sidecars)
	if err != nil {
		setAgentSidecarProblem(n.Id, err.Error())
		return nil
	}
	if stats.SidecarsRevision == agentproto.SidecarsRevision(want) {
		setAgentSidecarProblem(n.Id, "")
		return nil
	}
	s.mu.Lock()
	st := s.stateFor(n.Id)
	waiting := now.Before(st.sidecarsRetry)
	s.mu.Unlock()
	if waiting {
		return nil
	}
	client, err := rt.Client()
	if err != nil {
		return nil // the config sync of the same tick says why there is no client
	}

	_, err = client.PutSidecars(ctx, want)
	var answer *runtime.AgentError
	problem, retryAfter := "", agentRetryAfter
	switch {
	case err == nil:
		retryAfter = 0
	case errors.As(err, &answer) && answer.Status == http.StatusNotFound:
		// A 404 to a valid request is how an agent answers a path it does not know.
		problem, retryAfter = agentTooOldForSidecars, agentRefusalRetryAfter
	case errors.As(err, &answer) && answer.Status == http.StatusBadRequest:
		problem, retryAfter = "the agent refused the helper programs: "+answer.Message, agentRefusalRetryAfter
	}
	s.mu.Lock()
	st.sidecarsRetry = now.Add(retryAfter)
	s.mu.Unlock()
	setAgentSidecarProblem(n.Id, problem)
	if err != nil && problem == "" {
		return fmt.Errorf("set the helper programs: %w", err)
	}
	return nil
}
