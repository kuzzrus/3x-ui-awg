package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// agentSiteResendAfter is how long a site that an agent took is left alone before it can be sent again. An agent that took it
// and still lists none would otherwise be sent the same megabytes on every tick.
const agentSiteResendAfter = time.Minute

// siteProblem is a reason on the panel's side that a decoy site cannot be sent, which the heartbeat shows as the node's problem.
type siteProblem struct{ reason string }

func (p *siteProblem) Error() string { return p.reason }

// sendDecoySite sends an agent the zip of the site its node's decoy names, in the background, so that a slow link does not
// hold the tick that waits for every node. The decoy itself goes out on the first tick after the site is there.
func (s *AgentSyncService) sendDecoySite(rt *runtime.AgentRuntime, n *model.Node, site string, now time.Time) {
	client, err := rt.Client()
	if err != nil {
		return // the config sync of the same tick says why there is no client
	}
	s.mu.Lock()
	st := s.stateFor(n.Id)
	if st.sendingSite || now.Before(st.siteRetryAt) {
		s.mu.Unlock()
		return
	}
	st.sendingSite = true
	s.mu.Unlock()

	go func() {
		s.finishDecoySite(n.Id, st, pushDecoySite(n.Name, client, site))
	}()
}

func pushDecoySite(nodeName string, client *runtime.AgentClient, site string) error {
	agentGeoSlots <- struct{}{}
	defer func() { <-agentGeoSlots }()
	zip, err := checkedDecoySiteZip(site)
	if err != nil {
		return &siteProblem{reason: err.Error()}
	}
	logger.Infof("agent %s lacks the decoy site %s, sending it", nodeName, site)
	stored, err := client.PutDecoySite(context.Background(), site, zip)
	if err != nil {
		return err
	}
	logger.Infof("agent %s holds the decoy site %s now (%d bytes)", nodeName, stored.Sha256, stored.Size)
	return nil
}

// finishDecoySite records how a send ended: an agent that cannot take the site is left alone for a while and the heartbeat says
// why, and a link that failed is tried again after longer waits.
func (s *AgentSyncService) finishDecoySite(nodeID int, st *agentSyncState, err error) {
	var local *siteProblem
	var answer *runtime.AgentError
	problem, wait := "", agentRefusalRetryAfter
	s.mu.Lock()
	defer s.mu.Unlock()
	st.sendingSite = false
	switch {
	case err == nil:
		st.siteFailures, st.siteRetryAt = 0, time.Now().Add(agentSiteResendAfter)
		agentDecoyProblems.set(nodeID, "")
		return
	case errors.As(err, &local):
		problem = local.Error()
	case errors.As(err, &answer) && answer.Status == http.StatusNotFound:
		problem = agentTooOldForDecoy
	case errors.As(err, &answer) && (answer.Status == http.StatusBadRequest || answer.Status == http.StatusRequestEntityTooLarge):
		problem = "the agent refused the decoy site: " + answer.Message
	default:
		st.siteFailures++
		wait = geoRetryDelay(st.siteFailures)
		logger.Warningf("send the decoy site to agent %d: %v", nodeID, err)
	}
	st.siteRetryAt = time.Now().Add(wait)
	if problem != "" {
		agentDecoyProblems.set(nodeID, problem)
	}
}
