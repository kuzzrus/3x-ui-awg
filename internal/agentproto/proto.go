// Package agentproto is the wire contract between a master panel and an
// x-ui-agent node: paths, JSON bodies, the pairing bundle and TLS identity.
package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
)

const (
	// PathConfig takes the rendered Xray config as the raw request body.
	PathConfig  = "/v1/config"
	PathStatus  = "/v1/status"
	PathStats   = "/v1/stats"
	PathRestart = "/v1/restart"

	// QueryRestartOnUserRemoval is the PathConfig query flag (a bool) that makes
	// the agent restart the core instead of dropping a removed client's credential.
	QueryRestartOnUserRemoval = "restartOnUserRemoval"

	// MaxConfigBytes caps a PUT /v1/config body on the agent.
	MaxConfigBytes = 32 << 20
)

// How a pushed config was applied.
const (
	AppliedNoop    = "noop"
	AppliedHot     = "hot"
	AppliedRestart = "restart"
)

// Same strings the panel reports for its own core.
const (
	XrayStateRunning = "running"
	XrayStateStopped = "stop"
	XrayStateError   = "error"
)

type ConfigResponse struct {
	Revision  string `json:"revision"`
	Applied   string `json:"applied"`
	XrayState string `json:"xrayState"`
}

type Status struct {
	AgentVersion   string  `json:"agentVersion"`
	Hostname       string  `json:"hostname"`
	Guid           string  `json:"guid"`
	ConfigRevision string  `json:"configRevision"`
	XrayVersion    string  `json:"xrayVersion"`
	XrayState      string  `json:"xrayState"`
	XrayError      string  `json:"xrayError"`
	CpuPct         float64 `json:"cpuPct"`
	MemPct         float64 `json:"memPct"`
	UptimeSecs     uint64  `json:"uptimeSecs"`
	NetUp          uint64  `json:"netUp"`
	NetDown        uint64  `json:"netDown"`
}

type Counter struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// Counters are cumulative since XrayStartedAt (unix ms, 0 when the core is down), so a
// changed start time marks a reset. ConfigRevision tells the master whether a push is due.
type Stats struct {
	ConfigRevision string             `json:"configRevision"`
	XrayStartedAt  int64              `json:"xrayStartedAt"`
	Inbounds       map[string]Counter `json:"inbounds"`
	Users          map[string]Counter `json:"users"`
	Online         []string           `json:"online"`
}

type ErrorBody struct {
	Error string `json:"error"`
}

// RevisionOf identifies a push by the exact bytes sent, so no JSON re-encoding
// on either end can make the master's and the agent's values disagree.
func RevisionOf(config []byte, restartOnUserRemoval bool) string {
	flag := byte(0)
	if restartOnUserRemoval {
		flag = 1
	}
	h := sha256.New()
	h.Write([]byte{flag})
	h.Write(config)
	return hex.EncodeToString(h.Sum(nil))
}
