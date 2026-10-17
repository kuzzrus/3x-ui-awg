package service

import (
	"math"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"

	"gorm.io/gorm"
)

var subBalancerStrategies = map[string]struct{}{
	"leastLoad":  {},
	"leastPing":  {},
	"random":     {},
	"roundRobin": {},
}

// SubBalancerService manages client-side JSON-subscription balancers; rows
// are read per request by internal/sub, so mutations need no xray restart.
type SubBalancerService struct{}

func (s *SubBalancerService) validate(b *model.SubBalancer) error {
	b.Remark = strings.TrimSpace(b.Remark)
	if b.Remark == "" {
		return common.NewError("balancer remark is required")
	}
	if len(b.Remark) > 256 {
		return common.NewError("balancer remark too long (max 256)")
	}
	if b.Strategy == "" {
		b.Strategy = "random"
	}
	if _, ok := subBalancerStrategies[b.Strategy]; !ok {
		return common.NewError("invalid balancer strategy:", b.Strategy)
	}
	if err := s.validateNodes(b); err != nil {
		return err
	}
	if len(b.InboundIds) == 0 && len(b.NodeIds) == 0 {
		return common.NewError("balancer must select at least one inbound or node")
	}
	if b.InboundIds == nil {
		// A balancer of nodes alone still lists its inbounds, as an empty array.
		b.InboundIds = []int{}
	}
	if err := s.validateWeights(b); err != nil {
		return err
	}
	if b.SortOrder < 1 {
		b.SortOrder = 1
	}
	return nil
}

// validateNodes drops repeated node ids and refuses one that names no node, so a
// mistyped id cannot sit in a balancer where it never matches anything.
func (s *SubBalancerService) validateNodes(b *model.SubBalancer) error {
	if len(b.NodeIds) == 0 {
		b.NodeIds = nil
		return nil
	}
	unique := make([]int, 0, len(b.NodeIds))
	for _, id := range b.NodeIds {
		if id < 1 {
			return common.NewError("balancer node id must be positive")
		}
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	var found int64
	if err := database.GetDB().Model(&model.Node{}).Where("id IN ?", unique).Count(&found).Error; err != nil {
		return err
	}
	if int(found) != len(unique) {
		return common.NewError("balancer selects a node that does not exist")
	}
	b.NodeIds = unique
	return nil
}

// DropNodeTx takes a deleted node out of every balancer that selects it, so none keeps an id
// that nothing can resolve. It runs in the transaction that deletes the node.
func (s *SubBalancerService) DropNodeTx(tx *gorm.DB, nodeID int) error {
	var balancers []model.SubBalancer
	if err := tx.Find(&balancers).Error; err != nil {
		return err
	}
	for i := range balancers {
		before := balancers[i].NodeIds
		balancers[i].NodeIds = slices.DeleteFunc(before, func(id int) bool { return id == nodeID })
		if len(balancers[i].NodeIds) == len(before) {
			continue
		}
		if err := tx.Save(&balancers[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

// validateWeights rejects weights xray cannot honor (non-positive, outside
// float32 range, non-leastLoad strategy) and drops stray inbound ids.
func (s *SubBalancerService) validateWeights(b *model.SubBalancer) error {
	if len(b.MemberWeights) == 0 {
		b.MemberWeights = nil
		return nil
	}
	if b.Strategy != "leastLoad" {
		return common.NewError("balancer weights only apply to the leastLoad strategy")
	}
	cleaned := make(map[int]float64, len(b.MemberWeights))
	for id, weight := range b.MemberWeights {
		if !slices.Contains(b.InboundIds, id) {
			continue
		}
		// xray decodes costs as float32; out-of-range values make it reject the
		// whole config, and underflow decays to the tag-digit fallback weight.
		if weight <= 0 || weight > math.MaxFloat32 || weight < math.SmallestNonzeroFloat32 {
			return common.NewError("balancer member weights must be a positive float32 value")
		}
		cleaned[id] = weight
	}
	if len(cleaned) == 0 {
		cleaned = nil
	}
	b.MemberWeights = cleaned
	return nil
}

// List returns all balancers in subscription order.
func (s *SubBalancerService) List() ([]*model.SubBalancer, error) {
	var balancers []*model.SubBalancer
	err := database.GetDB().Model(&model.SubBalancer{}).
		Order("sort_order asc, id asc").Find(&balancers).Error
	return balancers, err
}

func (s *SubBalancerService) Get(id int) (*model.SubBalancer, error) {
	var balancer model.SubBalancer
	if err := database.GetDB().First(&balancer, id).Error; err != nil {
		return nil, err
	}
	return &balancer, nil
}

func (s *SubBalancerService) Create(balancer *model.SubBalancer) (*model.SubBalancer, error) {
	if err := s.validate(balancer); err != nil {
		return nil, err
	}
	if err := database.GetDB().Create(balancer).Error; err != nil {
		return nil, err
	}
	return balancer, nil
}

func (s *SubBalancerService) Update(id int, balancer *model.SubBalancer, enabled *bool) (*model.SubBalancer, error) {
	if err := s.validate(balancer); err != nil {
		return nil, err
	}
	current, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	current.Remark = balancer.Remark
	current.Strategy = balancer.Strategy
	current.InboundIds = balancer.InboundIds
	current.NodeIds = balancer.NodeIds
	current.MemberWeights = balancer.MemberWeights
	current.SortOrder = balancer.SortOrder
	if enabled != nil {
		current.Enabled = *enabled
	}
	if err := database.GetDB().Save(current).Error; err != nil {
		return nil, err
	}
	return current, nil
}

func (s *SubBalancerService) Delete(id int) error {
	res := database.GetDB().Delete(&model.SubBalancer{}, id)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return common.NewError("sub balancer not found")
	}
	return nil
}
