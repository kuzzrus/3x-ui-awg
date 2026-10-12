package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// agentSetting is something an agent is told apart from its config and kept told: which helper programs
// to run, which decoy to serve. It is sent when the agent reports another revision of it than the node asks for.
type agentSetting struct {
	what     string // for the messages
	tooOld   string // what the heartbeat says of an agent that predates it
	problems *problemSet
	retry    func(*agentSyncState) *time.Time // not sent again before then
	send     func(ctx context.Context, client *runtime.AgentClient) error
}

// keepSetting sends the setting unless the agent holds it, and leaves one that cannot take it alone for a
// while, the heartbeat saying why.
func (s *AgentSyncService) keepSetting(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, now time.Time, held bool, set agentSetting) error {
	if held {
		set.problems.set(n.Id, "")
		return nil
	}
	s.mu.Lock()
	retryAt := set.retry(s.stateFor(n.Id))
	waiting := now.Before(*retryAt)
	s.mu.Unlock()
	if waiting {
		return nil
	}
	client, err := rt.Client()
	if err != nil {
		return nil // the config sync of the same tick says why there is no client
	}

	err = set.send(ctx, client)
	var answer *runtime.AgentError
	problem, retryAfter := "", agentRetryAfter
	switch {
	case err == nil:
		retryAfter = 0
	case errors.As(err, &answer) && answer.Status == http.StatusNotFound:
		// A 404 to a valid request is how an agent answers a path it does not know.
		problem, retryAfter = set.tooOld, agentRefusalRetryAfter
	case errors.As(err, &answer) && answer.Status == http.StatusBadRequest:
		problem, retryAfter = "the agent refused the "+set.what+": "+answer.Message, agentRefusalRetryAfter
	}
	s.mu.Lock()
	*set.retry(s.stateFor(n.Id)) = now.Add(retryAfter)
	s.mu.Unlock()
	set.problems.set(n.Id, problem)
	if err != nil && problem == "" {
		return fmt.Errorf("set the %s: %w", set.what, err)
	}
	return nil
}
