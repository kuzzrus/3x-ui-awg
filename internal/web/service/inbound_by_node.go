package service

import (
	"errors"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// GetInboundsByNode is GetAllInbounds for one node's inbounds, in id order so that what is
// rendered from them is the same bytes every time.
func (s *InboundService) GetInboundsByNode(nodeID int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("node_id = ?", nodeID).Order("id ASC").Preload("ClientStats").Find(&inbounds).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	s.enrichClientStats(db, inbounds)
	return inbounds, nil
}
