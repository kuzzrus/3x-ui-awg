package model

// What an AgentCounter counts.
const (
	AgentCounterUser    = "user"
	AgentCounterInbound = "inbound"
)

// AgentCounter is the last value accounted of one cumulative counter of an agent's core. It is not
// node_client_traffics, whose rows a usage reset drops, which would count the whole counter again.
type AgentCounter struct {
	Id     int    `json:"id" gorm:"primaryKey;autoIncrement"`
	NodeId int    `json:"nodeId" gorm:"uniqueIndex:idx_agent_counter,priority:1;not null"`
	Kind   string `json:"kind" gorm:"uniqueIndex:idx_agent_counter,priority:2;not null"`
	Name   string `json:"name" gorm:"uniqueIndex:idx_agent_counter,priority:3;not null"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}
