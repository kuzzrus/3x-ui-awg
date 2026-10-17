package sub

import (
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// NodeOutbounds writes the outbound of each enabled inbound that the client with this email has on the nodes, as
// the JSON subscription writes it for a client app. The panel's own core dials the nodes with them.
func NodeOutbounds(email string, nodeIDs []int) ([]service.NodeOutbound, error) {
	email = strings.TrimSpace(email)
	if email == "" || len(nodeIDs) == 0 {
		return nil, nil
	}
	db := database.GetDB()
	type linked struct {
		model.ClientRecord
		FlowOverride string
		InboundID    int
	}
	var rows []linked
	err := db.Table("clients").
		Select("clients.*, client_inbounds.flow_override AS flow_override, client_inbounds.inbound_id AS inbound_id").
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
		Where("LOWER(clients.email) = ? AND clients.enable = ? AND inbounds.enable = ? AND inbounds.node_id IN ?",
			strings.ToLower(email), true, true, nodeIDs).
		Order("inbounds.node_id ASC, inbounds.id ASC").
		Find(&rows).Error
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	inboundIDs := make([]int, len(rows))
	for i := range rows {
		inboundIDs[i] = rows[i].InboundID
	}
	var inbounds []*model.Inbound
	if err := db.Where("id IN ?", inboundIDs).Find(&inbounds).Error; err != nil {
		return nil, err
	}
	byID := make(map[int]*model.Inbound, len(inbounds))
	for _, inbound := range inbounds {
		byID[inbound.Id] = inbound
	}

	js := NewSubJsonService("", "", "", "", NewSubService(""))
	req := js.SubService.ForRequest("")
	var out []service.NodeOutbound
	for i := range rows {
		inbound := byID[rows[i].InboundID]
		if inbound == nil || inbound.NodeID == nil {
			continue
		}
		nodeID := *inbound.NodeID
		client := *rows[i].ToClient()
		client.Flow = rows[i].FlowOverride
		req.projectThroughFallbackMaster(inbound)
		// One outbound per inbound: the first endpoint, which is the one a client app lists first.
		for _, doc := range js.getConfig(req, inbound, client, req.address) {
			if proxy := extractProxyOutbound(doc); proxy != nil {
				out = append(out, service.NodeOutbound{NodeID: nodeID, ClientID: rows[i].Id, InboundID: inbound.Id, Outbound: proxy})
				break
			}
		}
	}
	return out, nil
}
