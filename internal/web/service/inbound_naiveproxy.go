package service

import (
	"context"
	"net/mail"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// DesiredNaiveProxyInstances mirrors DesiredMtprotoInstances: one instance
// per enabled inbound, clients filtered by client_traffics depletion too.
func (s *InboundService) DesiredNaiveProxyInstances() ([]naiveproxy.Instance, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).
		Where("protocol = ? AND enable = ? AND node_id IS NULL", model.NaiveProxy, true).
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

	instances := make([]naiveproxy.Instance, 0, len(inbounds))
	for _, ib := range inbounds {
		inst, ok := naiveproxy.InstanceFromInbound(ib)
		if !ok {
			continue
		}
		if off := disabled[ib.Id]; len(off) > 0 {
			kept := make([]naiveproxy.Client, 0, len(inst.Clients))
			for _, c := range inst.Clients {
				if _, skip := off[c.Email]; !skip {
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

// applyLocalNaiveProxy mirrors applyLocalMtproto exactly, including the
// buildInboundForLocalRuntime step so a just-depleted client isn't pushed as enabled.
func (s *InboundService) applyLocalNaiveProxy(inboundId int) {
	inbound, err := s.GetInbound(inboundId)
	if err != nil || inbound == nil || inbound.Protocol != model.NaiveProxy || inbound.NodeID != nil {
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
		logger.Debug("naiveproxy: immediate client apply failed for inbound", inboundId, ":", err)
	}
}

// NaiveProxyCertTarget is the certificate setup of one local NaiveProxy inbound.
type NaiveProxyCertTarget struct {
	InboundId int
	Enable    bool
	naiveproxy.CertSettings
}

// NaiveProxyCertTargets lists the certificate setup of every local NaiveProxy
// inbound, enabled or not, in inbound order so the first of two clashing domains always wins.
func (s *InboundService) NaiveProxyCertTargets() ([]NaiveProxyCertTarget, error) {
	var inbounds []*model.Inbound
	err := database.GetDB().Model(model.Inbound{}).
		Where("protocol = ? AND node_id IS NULL", model.NaiveProxy).
		Order("id").
		Find(&inbounds).Error
	if err != nil {
		return nil, err
	}
	targets := make([]NaiveProxyCertTarget, 0, len(inbounds))
	for _, ib := range inbounds {
		settings, ok := naiveproxy.CertSettingsFromInbound(ib)
		if !ok {
			continue
		}
		targets = append(targets, NaiveProxyCertTarget{InboundId: ib.Id, Enable: ib.Enable, CertSettings: settings})
	}
	return targets, nil
}

// validateNaiveProxyCert refuses an automatic-certificate inbound Let's Encrypt could never
// serve: a domain it cannot validate over HTTP-01, or a contact address that is not one.
func validateNaiveProxyCert(inbound *model.Inbound) error {
	settings, ok := naiveproxy.CertSettingsFromInbound(inbound)
	if !ok || settings.Mode != naiveproxy.CertAuto {
		return nil
	}
	if err := naiveproxy.ValidCertDomain(settings.Domain); err != nil {
		return common.NewErrorf("naiveproxy automatic certificate: %v", err)
	}
	if email := strings.TrimSpace(settings.Email); email != "" {
		if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
			return common.NewErrorf("naiveproxy automatic certificate: %q is not an email address", settings.Email)
		}
	}
	return nil
}
