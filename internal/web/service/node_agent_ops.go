package service

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// A restart is asked for by a request that the panel cuts off after 30 s, whichever kind of node it
// is; the call that reads an agent's log is bound by the client, at 10 s.
const nodeRestartTimeout = 25 * time.Second

// AgentLogs reads the newest lines of an agent node's log, which holds what its core printed too.
func (s *NodeService) AgentLogs(id, count int, level string) ([]string, error) {
	n, err := s.GetById(id)
	if err != nil {
		return nil, err
	}
	if n.Kind != model.NodeKindAgent {
		return nil, errors.New("only an agent node has a log to read from here")
	}
	mgr := runtime.GetManager()
	if mgr == nil {
		return nil, errors.New("runtime manager unavailable")
	}
	rt, err := mgr.AgentFor(n)
	if err != nil {
		return nil, err
	}
	client, err := rt.Client()
	if err != nil {
		return nil, err
	}
	logs, err := client.Logs(context.Background(), count, level)
	var answer *runtime.AgentError
	if errors.As(err, &answer) && answer.Status == http.StatusNotFound {
		// A 404 to a valid request is how an agent answers a path it does not know.
		return nil, errors.New("this agent is too old to serve its log: update it")
	}
	if err != nil {
		return nil, err
	}
	return logs.Lines, nil
}

// RestartNodeXray restarts the Xray core of a node, whichever kind of node it is: an agent restarts
// into the config it last ran, a panel node through its own API.
func (s *NodeService) RestartNodeXray(id int) error {
	n, err := s.GetById(id)
	if err != nil {
		return err
	}
	if !n.Enable {
		return errors.New("node is disabled")
	}
	mgr := runtime.GetManager()
	if mgr == nil {
		return errors.New("runtime manager unavailable")
	}
	rt, err := mgr.RuntimeFor(&id)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), nodeRestartTimeout)
	defer cancel()
	return rt.RestartXray(ctx)
}
