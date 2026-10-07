// Package agentproto is the wire contract between a master panel and an
// x-ui-agent node: paths, JSON bodies, the pairing bundle and TLS identity.
package agentproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	PathConfig  = "/v1/config"
	PathStatus  = "/v1/status"
	PathStats   = "/v1/stats"
	PathRestart = "/v1/restart"

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

type ConfigRequest struct {
	Revision             string          `json:"revision"`
	Config               json.RawMessage `json:"config"`
	RestartOnUserRemoval bool            `json:"restartOnUserRemoval"`
}

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

// Stats counters are cumulative since XrayStartedAt (unix seconds, 0 when the
// core is not running), so a changed start time marks a reset exactly.
type Stats struct {
	XrayStartedAt int64              `json:"xrayStartedAt"`
	Inbounds      map[string]Counter `json:"inbounds"`
	Users         map[string]Counter `json:"users"`
	Online        []string           `json:"online"`
}

type ErrorBody struct {
	Error string `json:"error"`
}

// RevisionOf identifies a push by its content. The config is compacted first
// because encoding/json compacts a RawMessage on the wire.
func RevisionOf(config json.RawMessage, restartOnUserRemoval bool) (string, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, config); err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(compact.Bytes())
	if restartOnUserRemoval {
		h.Write([]byte{1})
	} else {
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
