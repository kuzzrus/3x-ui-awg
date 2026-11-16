package eventbus

import (
	"strings"
	"time"
)

// EventType identifies the kind of event flowing through the bus.
type EventType string

const (
	// Outbound health (observatory-driven)
	EventOutboundDown EventType = "outbound.down"
	EventOutboundUp   EventType = "outbound.up"

	// Xray core (local)
	EventXrayCrash EventType = "xray.crash"

	// Node health (heartbeat-driven)
	EventNodeDown EventType = "node.down"
	EventNodeUp   EventType = "node.up"

	// System health
	EventCPUHigh    EventType = "cpu.high"
	EventMemoryHigh EventType = "memory.high"

	// Security
	EventLoginAttempt EventType = "login.attempt"

	// Psiphon (scan-driven)
	EventPsiphonRegionsNew EventType = "psiphon.regions.new"
)

// Event is the unit of information flowing through the bus.
type Event struct {
	Type      EventType
	Source    string    // outbound tag, node name, client email, IP, etc.
	Data      any       // event-specific payload, may be nil
	Timestamp time.Time // when the event was detected
}

// OutboundHealthData carries observatory details for outbound events.
type OutboundHealthData struct {
	Delay int64  // last measured delay in ms, 0 if unknown
	Error string // last error if probe failed, empty if up
}

// NodeHealthData carries heartbeat details for node events.
type NodeHealthData struct {
	NodeId    int
	LatencyMs int
	CpuPct    float64
	MemPct    float64
	XrayState string // "running", "stopped", etc.
	XrayError string
}

// LoginEventData carries login attempt details.
type LoginEventData struct {
	Username string
	IP       string
	Time     string
	Status   string // "success" or "fail"
	Reason   string
}

// PsiphonRegion is one region of the Psiphon picker: its code and its name.
type PsiphonRegion struct {
	Code string
	Name string
}

// PsiphonRegionsData carries the regions that connected for the first time in a scan.
type PsiphonRegionsData struct {
	Regions []PsiphonRegion
}

// Names lists the regions as a message writes them: "Germany (DE), Japan (JP)".
func (d PsiphonRegionsData) Names() string {
	names := make([]string, len(d.Regions))
	for i, r := range d.Regions {
		names[i] = r.Name + " (" + r.Code + ")"
	}
	return strings.Join(names, ", ")
}

// SystemMetricData carries raw system metric values for threshold-based events.
type SystemMetricData struct {
	Percent   float64 // current usage percentage
	Threshold int     // configured threshold
}
