package service

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// DesiredTproxyInstances derives the tproxy engine configs this panel should
// run: one per enabled local inbound, excluding depleted clients.
func (s *InboundService) DesiredTproxyInstances() ([]tproxy.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.Tproxy, true).
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	if len(inbounds) == 0 {
		return nil, nil
	}

	ids := make([]int, 0, len(inbounds))
	for _, ib := range inbounds {
		ids = append(ids, ib.Id)
	}
	var disabledRows []xray.ClientTraffic
	err = db.Model(xray.ClientTraffic{}).
		Where("inbound_id IN ? AND enable = ?", ids, false).
		Select("inbound_id", "email").
		Find(&disabledRows).Error
	if err != nil {
		return nil, err
	}
	disabled := make(map[int]map[string]struct{}, len(disabledRows))
	for _, row := range disabledRows {
		if disabled[row.InboundId] == nil {
			disabled[row.InboundId] = map[string]struct{}{}
		}
		disabled[row.InboundId][row.Email] = struct{}{}
	}

	instances := make([]tproxy.Instance, 0, len(inbounds))
	for _, ib := range inbounds {
		inst, ok := tproxy.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		if off := disabled[ib.Id]; len(off) > 0 {
			kept := make([]tproxy.ClientSecret, 0, len(inst.Clients))
			for _, c := range inst.Clients {
				if _, skip := off[c.Name]; !skip {
					kept = append(kept, c)
				}
			}
			inst.Clients = kept
		}
		if len(inst.Clients) == 0 {
			continue
		}
		instances = append(instances, inst)
	}
	return instances, nil
}

// applyLocalTproxy pushes a single local tproxy inbound's current client set
// to its MTProxy engine right after a client edit commits. Mirrors
// applyLocalMtproto exactly, including its no-op conditions and the
// swallow-and-log-on-failure contract (the reconcile job is the backstop).
func (s *InboundService) applyLocalTproxy(inboundId int) {
	inbound, err := s.GetInbound(inboundId)
	if err != nil || inbound == nil || inbound.Protocol != model.Tproxy || inbound.NodeID != nil {
		return
	}
	rt, err := s.runtimeFor(inbound)
	if err != nil {
		return
	}
	payload := inbound
	if inbound.Enable {
		if built, bErr := s.buildInboundForLocalRuntime(database.GetDB(), inbound); bErr == nil {
			payload = built
		}
	}
	if err := rt.UpdateInbound(context.Background(), inbound, payload); err != nil {
		logger.Debug("tproxy: immediate client apply failed for inbound", inboundId, ":", err)
	}
}
