// Package agentproto is the wire contract between a master panel and an
// x-ui-agent node: paths, JSON bodies, the pairing bundle and TLS identity.
package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const (
	// PathConfig takes the rendered Xray config as the raw request body.
	PathConfig  = "/v1/config"
	PathStatus  = "/v1/status"
	PathStats   = "/v1/stats"
	PathRestart = "/v1/restart"
	// PathGeo lists the geo files the agent holds (GET) and stores one (PUT).
	PathGeo = "/v1/geo"
	// PathLogs returns the last lines of the agent's own log, which holds what its core printed too.
	PathLogs = "/v1/logs"
	// PathUpdate starts the agent's self-update (POST) and reports how the last one went (GET).
	PathUpdate = "/v1/update"
	// PathSidecars sets which helper programs the agent runs beside its core (PUT).
	PathSidecars = "/v1/sidecars"
	// PathDecoy sets the decoy site the agent serves to whoever probes it (PUT).
	PathDecoy = "/v1/decoy"

	// QueryRestartOnUserRemoval is the PathConfig query flag (a bool) that makes
	// the agent restart the core instead of dropping a removed client's credential.
	QueryRestartOnUserRemoval = "restartOnUserRemoval"

	// QueryUpdateDev is the PathUpdate query flag (a bool) that moves the agent to the rolling dev
	// channel instead of the latest release.
	QueryUpdateDev = "dev"

	// The PathGeo PUT names the file and carries the SHA-256 of its content as query parameters.
	QueryGeoName   = "name"
	QueryGeoSha256 = "sha256"

	// The PathLogs query: how many lines, and the least severe level to include.
	QueryLogCount = "count"
	QueryLogLevel = "level"

	// MaxConfigBytes caps a PUT /v1/config body on the agent.
	MaxConfigBytes = 32 << 20
	// MaxGeoBytes caps one geo file, as it is stored.
	MaxGeoBytes = 256 << 20
	// MaxLogLines caps what one PathLogs request returns.
	MaxLogLines = 1000
	// MaxSidecarsBytes caps a PathSidecars body, which is a short list of names.
	MaxSidecarsBytes = 4 << 10
	// MaxDecoyBytes caps a PathDecoy body, which is a handful of short fields.
	MaxDecoyBytes = 16 << 10

	maxGeoNameLen = 100
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
	// Sidecars is how each helper program the agent was told to run stands; an agent that has
	// none to run, and one that predates them, report an empty list.
	Sidecars []SidecarState `json:"sidecars"`
	// Decoy is how the decoy stands; an agent that predates it reports no state at all.
	Decoy DecoyState `json:"decoy"`
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
	// SidecarsRevision is SidecarsRevision of the helper programs the agent was told to run, which
	// tells the master whether it must send the list again. It is empty for none, as for an old agent.
	SidecarsRevision string `json:"sidecarsRevision"`
	// DecoyRevision is DecoyRevision of the decoy the agent was told to serve, empty for none and for an old agent.
	DecoyRevision string `json:"decoyRevision"`
}

// The helper programs an agent runs, each a client on a loopback SOCKS port for an outbound of the
// config to send traffic through: Tor on 19050 and Psiphon on 19060, the ports the panel's own use.
const (
	SidecarTor     = "tor"
	SidecarPsiphon = "psiphon"
)

// KnownSidecars are the helper programs an agent can run, in the order they are listed.
var KnownSidecars = []string{SidecarTor, SidecarPsiphon}

// How a helper program stands on the agent; pending is one that is listed and not looked at yet.
const (
	SidecarPending    = "pending"
	SidecarInstalling = "installing"
	SidecarRunning    = "running"
	SidecarFailed     = "failed"
)

// SidecarsSpec is the body of PathSidecars: the helper programs the agent should run. The list
// replaces the one before it, and an empty one stops them all.
type SidecarsSpec struct {
	Enabled []string `json:"enabled"`
}

// SidecarState is how one helper program stands. Message is the last thing it said or why it
// failed, which for Tor tells how far it got in building a circuit.
type SidecarState struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// Sidecars is the answer of PathSidecars: the list as the agent keeps it, and how each stands.
type Sidecars struct {
	Revision string         `json:"revision"`
	Enabled  []string       `json:"enabled"`
	States   []SidecarState `json:"states"`
}

// UnknownSidecarError is a helper program that no agent of this version runs.
type UnknownSidecarError struct{ Name string }

func (e *UnknownSidecarError) Error() string { return fmt.Sprintf("unknown helper program %q", e.Name) }

// NormalizeSidecars checks a list of helper programs against KnownSidecars and returns it without
// repeats, in their listed order, the form both ends compare and store; none is nil.
func NormalizeSidecars(names []string) ([]string, error) {
	want := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !slices.Contains(KnownSidecars, name) {
			return nil, &UnknownSidecarError{Name: name}
		}
		want[name] = true
	}
	var out []string
	for _, name := range KnownSidecars {
		if want[name] {
			out = append(out, name)
		}
	}
	return out, nil
}

// SidecarsRevision identifies a normalized list, and is empty for none, which is what an agent
// that has never been given one reports.
func SidecarsRevision(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.Join(names, ",")))
	return hex.EncodeToString(sum[:])
}

// How a self-update run stands. None is what an agent that never ran one reports.
const (
	UpdateNone    = "none"
	UpdatePending = "pending"
	UpdateSuccess = "success"
	UpdateFailed  = "failed"
)

// UpdateStatus is one self-update run. RunID is a decimal string, so that no JSON reader rounds it.
type UpdateStatus struct {
	RunID      string `json:"runId"`
	State      string `json:"state"`
	ExitCode   int    `json:"exitCode"`
	StartedAt  int64  `json:"startedAt,omitempty"`
	FinishedAt int64  `json:"finishedAt,omitempty"`
}

// Logs is the answer of PathLogs, the newest line first as the panel serves its own log.
type Logs struct {
	Lines []string `json:"lines"`
}

// GeoFile is a geo database in the agent's asset folder.
type GeoFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

type GeoFiles struct {
	Files []GeoFile `json:"files"`
}

var geoNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+\.dat$`)

// ValidGeoName reports whether name can be a geo file in the asset folder: plain characters, no
// path and no dot-dot, ending in .dat, the rule the panel applies to the geo files it updates.
func ValidGeoName(name string) bool {
	return len(name) <= maxGeoNameLen && !strings.Contains(name, "..") && geoNamePattern.MatchString(name)
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
