package model

// What an AgentCounter counts.
const (
	AgentCounterUser    = "user"
	AgentCounterInbound = "inbound"
)

// AgentCounter is the last cumulative value the master accounted for one counter of an
// agent's core, so the next poll can tell new traffic from what it already counted. It is
// its own table, not node_client_traffics: those rows are dropped whenever a client's
// usage is reset on the master, which would make the agent's whole counter count again.
type AgentCounter struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId int    `json:"nodeId" gorm:"uniqueIndex:idx_agent_counter,priority:1;not null"`
	Kind   string `json:"kind" gorm:"uniqueIndex:idx_agent_counter,priority:2;not null"`
	Name   string `json:"name" gorm:"uniqueIndex:idx_agent_counter,priority:3;not null"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}
