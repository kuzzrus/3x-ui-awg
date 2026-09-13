package service

import (
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
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
		clients, cErr := s.GetClients(ib)
		if cErr != nil {
			continue
		}
		off := disabled[ib.Id]
		secrets := make([]tproxy.ClientSecret, 0, len(clients))
		for _, c := range clients {
			if !c.Enable || c.TproxySecret == "" || c.Email == "" {
				continue
			}
			if _, skip := off[c.Email]; skip {
				continue
			}
			secrets = append(secrets, tproxy.ClientSecret{Name: c.Email, Secret: c.TproxySecret})
		}
		if len(secrets) == 0 {
			continue
		}
		instances = append(instances, tproxy.Instance{Id: ib.Id, Clients: secrets})
	}
	return instances, nil
}
