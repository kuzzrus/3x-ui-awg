package service

import (
	"slices"
	"strings"
	"sync"
)

// problemSet holds, per agent node, what is wrong with one thing about it. The sync has only the log to
// tell, so the heartbeat shows it as the last error.
type problemSet struct{ m sync.Map } // node id -> string

// set records what is wrong, and forgets it when problem is empty.
func (p *problemSet) set(nodeID int, problem string) {
	if problem == "" {
		p.m.Delete(nodeID)
		return
	}
	p.m.Store(nodeID, problem)
}

func (p *problemSet) get(nodeID int) string {
	problem, _ := p.m.Load(nodeID)
	text, _ := problem.(string)
	return text
}

// What is wrong with the config of a node as last built (why it could not be built, or what had to be adjusted),
// with its helper programs, and with its decoy.
var (
	agentConfigProblems  problemSet
	agentSidecarProblems problemSet
	agentDecoyProblems   problemSet
)

func setAgentConfigProblem(nodeID int, problem string) { agentConfigProblems.set(nodeID, problem) }
func agentConfigProblem(nodeID int) string             { return agentConfigProblems.get(nodeID) }

func setAgentSidecarProblem(nodeID int, problem string) { agentSidecarProblems.set(nodeID, problem) }
func agentSidecarProblem(nodeID int) string             { return agentSidecarProblems.get(nodeID) }

func setAgentDecoyProblem(nodeID int, problem string) { agentDecoyProblems.set(nodeID, problem) }

// agentProblems is the last error the heartbeat shows for an agent node: what is wrong with its config,
// its helper programs and its decoy.
func agentProblems(nodeID int) string {
	problems := []string{agentConfigProblem(nodeID), agentSidecarProblem(nodeID), agentDecoyProblems.get(nodeID)}
	return strings.Join(slices.DeleteFunc(problems, func(problem string) bool { return problem == "" }), "; ")
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
