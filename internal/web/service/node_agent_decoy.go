package service

import (
	"context"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const agentTooOldForDecoy = "this agent is too old to serve a decoy: update it"

// syncDecoy sends an agent the decoy its node asks for when the agent reports another one, and an agent
// that predates decoys reports none, which is what a node that asks for none wants.
func (s *AgentSyncService) syncDecoy(ctx context.Context, rt *runtime.AgentRuntime, n *model.Node, stats *agentproto.Stats, now time.Time) error {
	if stats == nil {
		return nil
	}
	want, err := normalizeNodeDecoy(n.Decoy)
	if err != nil {
		agentDecoyProblems.set(n.Id, err.Error())
		return nil
	}
	spec := decoySpecOf(want)
	return s.keepSetting(ctx, rt, n, now, stats.DecoyRevision == agentproto.DecoyRevision(spec), agentSetting{
		what:     "decoy",
		tooOld:   agentTooOldForDecoy,
		problems: &agentDecoyProblems,
		retry:    func(st *agentSyncState) *time.Time { return &st.decoyRetry },
		send: func(ctx context.Context, client *runtime.AgentClient) error {
			_, err := client.PutDecoy(ctx, spec)
			return err
		},
	})
}
