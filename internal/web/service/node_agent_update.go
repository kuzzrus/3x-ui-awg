package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const (
	// The agent downloads the installer before it answers that the update has started.
	agentUpdateStartTimeout = time.Minute
	agentUpdatePollTimeout  = 10 * time.Second
	// The installer downloads a release of tens of megabytes and the agent restarts at the end.
	agentUpdateWatchFor = 15 * time.Minute
)

// agentUpdateWatchEvery is a variable so a test can shorten it.
var agentUpdateWatchEvery = 5 * time.Second

// updateAgent has the agent run its installer again, and logs how that ends: the new version shows
// on the node by itself, but a failure would otherwise leave no trace on the master.
func (s *NodeService) updateAgent(mgr *runtime.Manager, n *model.Node, dev bool) error {
	rt, err := mgr.AgentFor(n)
	if err != nil {
		return err
	}
	client, err := rt.Client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentUpdateStartTimeout)
	defer cancel()
	status, err := client.Update(ctx, dev)
	var answer *runtime.AgentError
	if errors.As(err, &answer) && answer.Status == http.StatusNotFound {
		// A 404 to a valid request is how an agent answers a path it does not know.
		return errors.New("this agent is too old to update itself: run install-agent.sh on the node once")
	}
	if err != nil {
		return err
	}
	go watchAgentUpdate(n.Name, client, status.RunID)
	return nil
}

func watchAgentUpdate(name string, client *runtime.AgentClient, runID string) {
	for deadline := time.Now().Add(agentUpdateWatchFor); time.Now().Before(deadline); {
		time.Sleep(agentUpdateWatchEvery)
		ctx, cancel := context.WithTimeout(context.Background(), agentUpdatePollTimeout)
		status, err := client.UpdateStatus(ctx)
		cancel()
		// The agent is down while the installer swaps it, and answers again when the new one is up.
		if err != nil || status.RunID != runID || status.State == agentproto.UpdatePending {
			continue
		}
		if status.State == agentproto.UpdateSuccess {
			logger.Infof("agent %s updated", name)
		} else {
			logger.Warningf("the update of agent %s failed with exit code %d: journalctl -u x-ui-agent-update-%s on the node says why", name, status.ExitCode, runID)
		}
		return
	}
	logger.Warningf("agent %s did not report how its update ended", name)
}
