package service

import (
	"time"

	"gorm.io/gorm"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/crypto/nodetoken"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// AgentNodeRequest is what adding an agent node needs: where the agent will be reachable.
// Its secret and certificate are minted here, not chosen by the admin.
type AgentNodeRequest struct {
	Name                string `json:"name" form:"name" validate:"required"`
	Remark              string `json:"remark" form:"remark"`
	Address             string `json:"address" form:"address" validate:"required"`
	Port                int    `json:"port" form:"port" validate:"gte=1,lte=65535"`
	AllowPrivateAddress bool   `json:"allowPrivateAddress" form:"allowPrivateAddress"`
	OutboundTag         string `json:"outboundTag" form:"outboundTag"`
}

// AgentPairing is an agent node with the bundle that pairs its agent to this master. The
// bundle holds the agent's private key and secret: it is shown once and never stored.
type AgentPairing struct {
	Node   *NodeView `json:"node"`
	Bundle string    `json:"bundle" example:"xab1.eyJ2IjoxLCJhZGRyZXNzIjoibm9kZS5leGFtcGxlLmNvbSJ9"`
}

// CreateAgent adds an agent node: it mints the agent's secret and certificate, stores the secret and
// the pin, and marks the node dirty so the first config goes out as soon as the agent is up.
func (s *NodeService) CreateAgent(req *AgentNodeRequest) (*AgentPairing, error) {
	if req == nil {
		return nil, common.NewError("node request is required")
	}
	address, err := netsafe.NormalizeHost(req.Address)
	if err != nil {
		return nil, common.NewError(err.Error())
	}
	bundle, pin, err := agentproto.NewBundle(address, req.Port, time.Now())
	if err != nil {
		return nil, common.NewError(err.Error())
	}
	token, err := bundle.Encode()
	if err != nil {
		return nil, err
	}

	now := time.Now().UnixMilli()
	node := &model.Node{
		Name:                req.Name,
		Remark:              req.Remark,
		Kind:                model.NodeKindAgent,
		Scheme:              "https",
		Address:             address,
		Port:                req.Port,
		BasePath:            "/",
		ApiToken:            bundle.Secret,
		Enable:              true,
		AllowPrivateAddress: req.AllowPrivateAddress,
		TlsVerifyMode:       "pin",
		PinnedCertSha256:    pin,
		OutboundTag:         req.OutboundTag,
		ConfigDirty:         true,
		ConfigDirtyAt:       now,
	}
	if err := s.Create(node); err != nil {
		return nil, err
	}
	return &AgentPairing{Node: toNodeView(node), Bundle: token}, nil
}

// RepairAgent mints a new secret and certificate for an agent node, for an agent that was
// reinstalled or whose bundle was lost. The old agent stops being accepted at once.
func (s *NodeService) RepairAgent(id int) (*AgentPairing, error) {
	node, err := s.GetById(id)
	if err != nil {
		return nil, err
	}
	if node.Kind != model.NodeKindAgent {
		return nil, common.NewError("only an agent node has a pairing bundle")
	}
	bundle, pin, err := agentproto.NewBundle(node.Address, node.Port, time.Now())
	if err != nil {
		return nil, common.NewError(err.Error())
	}
	token, err := bundle.Encode()
	if err != nil {
		return nil, err
	}
	secret, err := nodetoken.Encrypt(id, bundle.Secret)
	if err != nil {
		return nil, err
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(model.Node{}).Where("id = ?", id).
			Updates(map[string]any{"api_token": secret, "pinned_cert_sha256": pin}).Error; err != nil {
			return err
		}
		return s.MarkNodeDirtyTx(tx, id)
	}); err != nil {
		return nil, err
	}
	if mgr := runtime.GetManager(); mgr != nil {
		mgr.InvalidateNode(id)
	}
	view, err := s.GetViewById(id)
	if err != nil {
		return nil, err
	}
	return &AgentPairing{Node: view, Bundle: token}, nil
}

// refuseAgentSecret rejects a request that carries a secret for an agent node, which only pairing changes.
func refuseAgentSecret(req *NodeMutationRequest) error {
	if req.ApiToken != nil || req.ClearApiToken {
		return common.NewError("an agent's secret changes only by pairing it again")
	}
	return nil
}

// keepAgentTransport puts back what a request cannot change on an agent node: how it is
// reached and which certificate is trusted come with its bundle.
func keepAgentTransport(stored, n *model.Node) {
	n.Scheme, n.BasePath = stored.Scheme, stored.BasePath
	n.TlsVerifyMode, n.PinnedCertSha256 = stored.TlsVerifyMode, stored.PinnedCertSha256
	n.InboundSyncMode, n.InboundTags = stored.InboundSyncMode, stored.InboundTags
}
