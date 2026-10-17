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

// pullNodePort is the port that a node whose agent dials the master holds: nothing reaches it, but a node needs one.
const pullNodePort = 443

// AgentNodeRequest is what adding an agent node needs: where the agent will be reachable, or the master it dials.
// Its secret and certificate are minted here, not chosen by the admin.
type AgentNodeRequest struct {
	Name    string `json:"name" form:"name" validate:"required"`
	Remark  string `json:"remark" form:"remark"`
	Address string `json:"address" form:"address" validate:"required"`
	// Port is where the master reaches the agent, which an agent that dials the master does not have.
	Port                int      `json:"port" form:"port" validate:"omitempty,gte=1,lte=65535"`
	AllowPrivateAddress bool     `json:"allowPrivateAddress" form:"allowPrivateAddress"`
	OutboundTag         string   `json:"outboundTag" form:"outboundTag"`
	Tags                []string `json:"tags" form:"tags"`
	Sidecars            []string `json:"sidecars" form:"sidecars"`
	NodeDecoyForm
	// Pull makes the agent dial the master at MasterUrl, the panel's address as the node reaches it, base path
	// included. MasterPin is the SHA-256 of the panel's certificate, for a panel whose own is not a public one.
	Pull      bool   `json:"pull" form:"pull"`
	MasterUrl string `json:"masterUrl" form:"masterUrl"`
	MasterPin string `json:"masterPin" form:"masterPin"`
}

// AgentRepairRequest says how an agent node is paired again. Left empty, a node that the panel dials is paired as it was,
// while one that dials the panel needs its MasterUrl again. Pull changes the way: MasterUrl is needed for pull, Port for push.
type AgentRepairRequest struct {
	Pull      *bool  `json:"pull" form:"pull"`
	Port      int    `json:"port" form:"port" validate:"omitempty,gte=1,lte=65535"`
	MasterUrl string `json:"masterUrl" form:"masterUrl"`
	MasterPin string `json:"masterPin" form:"masterPin"`
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
	node := &model.Node{
		Name:                req.Name,
		Remark:              req.Remark,
		Kind:                model.NodeKindAgent,
		Pull:                req.Pull,
		Scheme:              "https",
		Address:             address,
		Port:                req.Port,
		BasePath:            "/",
		Enable:              true,
		AllowPrivateAddress: req.AllowPrivateAddress,
		OutboundTag:         req.OutboundTag,
		Tags:                req.Tags,
		Sidecars:            req.Sidecars,
		Decoy:               req.decoy(),
		ConfigDirty:         true,
		ConfigDirtyAt:       time.Now().UnixMilli(),
	}
	if req.Pull {
		return s.createPullAgent(node, req)
	}
	bundle, pin, err := agentproto.NewBundle(address, req.Port, time.Now())
	if err != nil {
		return nil, common.NewError(err.Error())
	}
	token, err := bundle.Encode()
	if err != nil {
		return nil, err
	}
	node.ApiToken, node.TlsVerifyMode, node.PinnedCertSha256 = bundle.Secret, "pin", pin
	if err := s.Create(node); err != nil {
		return nil, err
	}
	return &AgentPairing{Node: toNodeView(node), Bundle: token}, nil
}

// createPullAgent adds a node whose agent dials the master. The bundle names the node, which has no id until
// it is stored, so the address of the master is checked first and the bundle is made once the node exists.
func (s *NodeService) createPullAgent(node *model.Node, req *AgentNodeRequest) (*AgentPairing, error) {
	secret, err := agentproto.NewSecret()
	if err != nil {
		return nil, err
	}
	if _, err := agentproto.NewPullBundle(req.MasterUrl, 1, secret, req.MasterPin); err != nil {
		return nil, common.NewError(err.Error())
	}
	if node.Port == 0 {
		node.Port = pullNodePort
	}
	node.ApiToken, node.TlsVerifyMode = secret, "verify"
	if err := s.Create(node); err != nil {
		return nil, err
	}
	bundle, err := agentproto.NewPullBundle(req.MasterUrl, node.Id, secret, req.MasterPin)
	if err != nil {
		return nil, err
	}
	token, err := bundle.Encode()
	if err != nil {
		return nil, err
	}
	return &AgentPairing{Node: toNodeView(node), Bundle: token}, nil
}

// RepairAgent mints a new secret, and a new certificate for a node the master dials, for an agent that was reinstalled,
// whose bundle was lost or that is to be paired the other way. The old agent stops being accepted at once.
func (s *NodeService) RepairAgent(id int, req *AgentRepairRequest) (*AgentPairing, error) {
	if req == nil {
		req = &AgentRepairRequest{}
	}
	node, err := s.GetById(id)
	if err != nil {
		return nil, err
	}
	if node.Kind != model.NodeKindAgent {
		return nil, common.NewError("only an agent node has a pairing bundle")
	}
	pull := node.Pull
	if req.Pull != nil {
		pull = *req.Pull
	}

	var secret, token string
	updates := map[string]any{"pull": pull}
	if pull {
		if secret, err = agentproto.NewSecret(); err != nil {
			return nil, err
		}
		bundle, err := agentproto.NewPullBundle(req.MasterUrl, id, secret, req.MasterPin)
		if err != nil {
			return nil, common.NewError(err.Error())
		}
		if token, err = bundle.Encode(); err != nil {
			return nil, err
		}
		updates["tls_verify_mode"], updates["pinned_cert_sha256"] = "verify", ""
	} else {
		port := node.Port
		if node.Pull {
			// The port of a node whose agent dials the master is only a placeholder, so the one to listen on is asked for.
			port = 0
		}
		if req.Port != 0 {
			port = req.Port
		}
		bundle, pin, err := agentproto.NewBundle(node.Address, port, time.Now())
		if err != nil {
			return nil, common.NewError(err.Error())
		}
		if token, err = bundle.Encode(); err != nil {
			return nil, err
		}
		secret = bundle.Secret
		updates["port"], updates["tls_verify_mode"], updates["pinned_cert_sha256"] = port, "pin", pin
	}
	if updates["api_token"], err = nodetoken.Encrypt(id, secret); err != nil {
		return nil, err
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(model.Node{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		return s.MarkNodeDirtyTx(tx, id)
	}); err != nil {
		return nil, err
	}
	runtime.GetAgentTunnels().Drop(id)
	if mgr := runtime.GetManager(); mgr != nil {
		mgr.InvalidateNode(id)
	}
	view, err := s.GetViewById(id)
	if err != nil {
		return nil, err
	}
	return &AgentPairing{Node: view, Bundle: token}, nil
}

// PullAgentSecret is the secret of the agent of node id when that node is an enabled agent that dials the master,
// and false for any other, so that the endpoint the agents dial treats all of them alike.
func (s *NodeService) PullAgentSecret(id int) (string, bool) {
	node, err := s.GetById(id)
	if err != nil || node.Kind != model.NodeKindAgent || !node.Pull || !node.Enable || node.ApiToken == "" {
		return "", false
	}
	return node.ApiToken, true
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
