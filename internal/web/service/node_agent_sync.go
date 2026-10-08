package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const (
	// A mutation marks the node dirty and is pushed on the next tick. The template, the
	// subscriptions and the settings do not, so every agent is compared this often.
	agentDriftCheckEvery = 30 * time.Second
	// How long a config the agent turned down is left alone before it is tried again.
	agentRefusalRetryAfter = 5 * time.Minute
	// How long after a push failed another is tried, unless a mutation asks for one sooner.
	agentRetryAfter = 15 * time.Second
)

// AgentSyncService keeps agent nodes running the config the master renders for them.
type AgentSyncService struct {
	xrayService    XrayService
	settingService SettingService
	nodeService    NodeService

	mu     sync.Mutex
	agents map[int]*agentSyncState
}

// agentSyncState is what the master remembers between ticks about one agent.
type agentSyncState struct {
	checkedAt      time.Time
	handledDirtyAt int64  // the dirty mark the last attempt dealt with, so a refusal is not retried every tick
	refused        string // revision the agent turned down
	refusedAt      time.Time
}

func (s *AgentSyncService) stateFor(nodeID int) *agentSyncState {
	if s.agents == nil {
		s.agents = map[int]*agentSyncState{}
	}
	st, ok := s.agents[nodeID]
	if !ok {
		st = &agentSyncState{}
		s.agents[nodeID] = st
	}
	return st
}

// Sync pushes the config an agent should run when its node is dirty or the periodic comparison
// finds another one; stats is the agent's report this tick, nil if unreadable. See the constants.
func (s *AgentSyncService) Sync(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, stats *agentproto.Stats) error {
	now := time.Now()
	s.mu.Lock()
	st := s.stateFor(n.Id)
	due := (n.ConfigDirty && n.ConfigDirtyAt != st.handledDirtyAt) || now.Sub(st.checkedAt) >= agentDriftCheckEvery
	if due {
		st.checkedAt, st.handledDirtyAt = now, n.ConfigDirtyAt
	}
	s.mu.Unlock()
	if !due {
		return nil
	}

	err := s.sync(ctx, rt, n, stats, now)
	if err != nil {
		s.mu.Lock()
		st.checkedAt = now.Add(agentRetryAfter - agentDriftCheckEvery)
		s.mu.Unlock()
	}
	return err
}

func (s *AgentSyncService) sync(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, stats *agentproto.Stats, now time.Time) error {
	client, err := rt.Client()
	if err != nil {
		return err
	}
	body, err := s.xrayService.RenderAgentConfig(n.Id)
	if err != nil {
		return fmt.Errorf("render the config: %w", err)
	}
	restartOnUserRemoval, err := s.settingService.GetRestartXrayOnClientDisable()
	if err != nil {
		return fmt.Errorf("read the restart policy: %w", err)
	}
	want := agentproto.RevisionOf(body, restartOnUserRemoval)

	if stats != nil && stats.ConfigRevision == want {
		s.clearDirty(n)
		return nil
	}
	s.mu.Lock()
	st := s.stateFor(n.Id)
	held := st.refused == want && now.Sub(st.refusedAt) < agentRefusalRetryAfter
	s.mu.Unlock()
	if held {
		return nil
	}

	_, err = client.PushConfig(ctx, body, restartOnUserRemoval)
	var answer *runtime.AgentError
	if errors.As(err, &answer) && answer.Refused() {
		s.mu.Lock()
		st.refused, st.refusedAt = want, now
		s.mu.Unlock()
		logger.Warningf("agent %s refused config %.8s, leaving it alone until it changes: %v", n.Name, want, err)
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	st.refused = ""
	s.mu.Unlock()
	s.clearDirty(n)
	return nil
}

func (s *AgentSyncService) clearDirty(n *model.Node) {
	if !n.ConfigDirty {
		return
	}
	if err := s.nodeService.ClearNodeDirty(n.Id, n.ConfigDirtyAt); err != nil {
		logger.Warningf("agent %s: clear dirty failed: %v", n.Name, err)
	}
}
