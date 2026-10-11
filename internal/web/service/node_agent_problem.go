package service

import (
	"strings"
	"sync"
)

// agentConfigProblems holds, per agent node, why its config could not be built on the last attempt.
// The sync has only the log to tell, so the heartbeat shows it as the last error of the node.
var agentConfigProblems sync.Map // node id -> string

// noteAgentConfigProblem records why the config of a node could not be built, and forgets it when err is nil.
func noteAgentConfigProblem(nodeID int, err error) {
	if err == nil {
		agentConfigProblems.Delete(nodeID)
		return
	}
	agentConfigProblems.Store(nodeID, "cannot build the config of this node: "+strings.TrimSpace(err.Error()))
}

// agentConfigProblem is why the last attempt to build the config of a node failed, or "".
func agentConfigProblem(nodeID int) string {
	problem, _ := agentConfigProblems.Load(nodeID)
	text, _ := problem.(string)
	return text
}
