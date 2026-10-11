package service

import (
	"slices"
	"strings"
	"sync"
)

// agentConfigProblems holds, per agent node, what is wrong with its config as last built: why it could not be
// built, or what had to be adjusted. The sync has only the log to tell, so the heartbeat shows it as the last error.
var agentConfigProblems sync.Map // node id -> string

// setAgentConfigProblem records what is wrong with the config of a node, and forgets it when problem is empty.
func setAgentConfigProblem(nodeID int, problem string) {
	if problem == "" {
		agentConfigProblems.Delete(nodeID)
		return
	}
	agentConfigProblems.Store(nodeID, problem)
}

// agentConfigProblem is what is wrong with the config of a node as last built, or "".
func agentConfigProblem(nodeID int) string {
	problem, _ := agentConfigProblems.Load(nodeID)
	text, _ := problem.(string)
	return text
}

// agentSidecarProblems holds, per agent node, why its helper programs could not be set, which the sync can only
// log and the heartbeat shows beside the problem with the config.
var agentSidecarProblems sync.Map // node id -> string

// setAgentSidecarProblem records why the helper programs of a node could not be set, and forgets it when problem is empty.
func setAgentSidecarProblem(nodeID int, problem string) {
	if problem == "" {
		agentSidecarProblems.Delete(nodeID)
		return
	}
	agentSidecarProblems.Store(nodeID, problem)
}

func agentSidecarProblem(nodeID int) string {
	problem, _ := agentSidecarProblems.Load(nodeID)
	text, _ := problem.(string)
	return text
}

// agentProblems is the last error the heartbeat shows for an agent node: what is wrong with its config
// and with its helper programs.
func agentProblems(nodeID int) string {
	problems := slices.DeleteFunc([]string{agentConfigProblem(nodeID), agentSidecarProblem(nodeID)}, func(problem string) bool { return problem == "" })
	return strings.Join(problems, "; ")
}

func unbuildableConfigProblem(err error) string {
	return "cannot build the config of this node: " + strings.TrimSpace(err.Error())
}

func adjustedConfigProblem(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "the config of this node was adjusted: " + strings.Join(notes, "; ")
}
