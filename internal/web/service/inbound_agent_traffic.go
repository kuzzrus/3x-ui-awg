package service

import (
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// agentTrafficBatch bounds one client_traffics lookup and one baseline insert, which both run into
// the database's variable limit past a few thousand.
const agentTrafficBatch = 1000

type agentCounterKey struct{ kind, name string }

// AddAgentTraffic adds to the totals what an agent's core counted since the last poll. The agent
// reports only cumulative counters, so the master keeps what it accounted of each and adds the rest.
func (s *InboundService) AddAgentTraffic(nodeID int, stats *agentproto.Stats) error {
	if nodeID <= 0 || stats == nil {
		return nil
	}
	if stats.XrayStartedAt == 0 {
		s.ClearNodeOnlineClients(nodeID)
		return nil
	}
	if err := submitTrafficWrite(func() error { return s.addAgentTrafficLocked(nodeID, stats) }); err != nil {
		s.ClearNodeOnlineClients(nodeID)
		return err
	}
	s.setAgentOnline(nodeID, stats.Online)
	return nil
}

func (s *InboundService) setAgentOnline(nodeID int, emails []string) {
	var node model.Node
	if err := database.GetDB().Select("id", "guid").Where("id = ?", nodeID).First(&node).Error; err != nil {
		return
	}
	s.SetNodeOnlineTree(nodeID, map[string][]string{effectiveNodeKey(&node): emails})
}

// agentDelta is how much a counter grew since the master last accounted it. A counter that
// began again (a new core, or a value below the one accounted) counts whole.
func agentDelta(cur, accounted int64, restarted bool) int64 {
	if restarted || cur < accounted {
		return cur
	}
	return cur - accounted
}

func (s *InboundService) addAgentTrafficLocked(nodeID int, stats *agentproto.Stats) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var node model.Node
		if err := tx.Select("id", "agent_started_at").Where("id = ?", nodeID).First(&node).Error; err != nil {
			return err
		}
		restarted := node.AgentStartedAt != stats.XrayStartedAt

		var rows []model.AgentCounter
		if err := tx.Where("node_id = ?", nodeID).Find(&rows).Error; err != nil {
			return err
		}
		accounted := make(map[agentCounterKey]model.AgentCounter, len(rows))
		for _, row := range rows {
			accounted[agentCounterKey{row.Kind, row.Name}] = row
		}

		var (
			clientDeltas  []*xray.ClientTraffic
			inboundDeltas = map[string]agentproto.Counter{}
			store         []model.AgentCounter
		)
		visit := func(kind, name string, cur agentproto.Counter) agentproto.Counter {
			before := accounted[agentCounterKey{kind, name}]
			if !restarted && cur.Up == before.Up && cur.Down == before.Down && before.Id != 0 {
				return agentproto.Counter{}
			}
			store = append(store, model.AgentCounter{NodeId: nodeID, Kind: kind, Name: name, Up: cur.Up, Down: cur.Down})
			return agentproto.Counter{Up: agentDelta(cur.Up, before.Up, restarted), Down: agentDelta(cur.Down, before.Down, restarted)}
		}
		for email, cur := range stats.Users {
			if d := visit(model.AgentCounterUser, email, cur); d.Up > 0 || d.Down > 0 {
				clientDeltas = append(clientDeltas, &xray.ClientTraffic{Email: email, Up: d.Up, Down: d.Down})
			}
		}
		for tag, cur := range stats.Inbounds {
			if d := visit(model.AgentCounterInbound, tag, cur); d.Up > 0 || d.Down > 0 {
				inboundDeltas[tag] = d
			}
		}

		for start := 0; start < len(clientDeltas); start += agentTrafficBatch {
			end := min(start+agentTrafficBatch, len(clientDeltas))
			if err := s.addClientTraffic(tx, clientDeltas[start:end]); err != nil {
				return err
			}
		}
		for tag, d := range inboundDeltas {
			if err := tx.Exec(
				fmt.Sprintf(`UPDATE inbounds SET up = %s, down = %s WHERE tag = ? AND node_id = ?`,
					database.ClampedAddExpr("up"), database.ClampedAddExpr("down")),
				d.Up, d.Down, tag, nodeID,
			).Error; err != nil {
				return err
			}
		}

		if restarted {
			// Counters of the previous core that the new one has not met yet would otherwise
			// be taken for what was accounted of a counter that began again.
			if err := tx.Where("node_id = ?", nodeID).Delete(&model.AgentCounter{}).Error; err != nil {
				return err
			}
			if err := tx.Model(model.Node{}).Where("id = ?", nodeID).Update("agent_started_at", stats.XrayStartedAt).Error; err != nil {
				return err
			}
		}
		if len(store) == 0 {
			return nil
		}
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "node_id"}, {Name: "kind"}, {Name: "name"}},
			DoUpdates: clause.AssignmentColumns([]string{"up", "down"}),
		}).CreateInBatches(store, agentTrafficBatch).Error
	})
}
