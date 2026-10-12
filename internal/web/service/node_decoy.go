package service

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const maxDecoyStateLen = 32

// NodeDecoyForm is the decoy of an agent node as a request carries it, one flat field each, since the
// pages post forms. No mode is no decoy.
type NodeDecoyForm struct {
	DecoyMode     string `json:"decoyMode" form:"decoyMode" example:"template"`
	DecoyTemplate string `json:"decoyTemplate" form:"decoyTemplate" example:"maintenance"`
	DecoyProxyURL string `json:"decoyProxyUrl" form:"decoyProxyUrl" example:""`
	DecoyDomain   string `json:"decoyDomain" form:"decoyDomain" example:"node.example.com"`
	DecoyCert     string `json:"decoyCert" form:"decoyCert" example:"auto"`
	DecoyEmail    string `json:"decoyEmail" form:"decoyEmail" example:"admin@example.com"`
	DecoyCertFile string `json:"decoyCertFile" form:"decoyCertFile" example:""`
	DecoyKeyFile  string `json:"decoyKeyFile" form:"decoyKeyFile" example:""`
	DecoyPort     int    `json:"decoyPort" form:"decoyPort" example:"7443"`
}

func (f NodeDecoyForm) decoy() model.NodeDecoy {
	return model.NodeDecoy{
		Mode: f.DecoyMode, Template: f.DecoyTemplate, ProxyURL: f.DecoyProxyURL, Domain: f.DecoyDomain,
		Cert: f.DecoyCert, Email: f.DecoyEmail, CertFile: f.DecoyCertFile, KeyFile: f.DecoyKeyFile, Port: f.DecoyPort,
	}
}

func decoySpecOf(d model.NodeDecoy) agentproto.DecoySpec {
	return agentproto.DecoySpec{
		Mode: d.Mode, Template: d.Template, ProxyURL: d.ProxyURL, Domain: d.Domain,
		Cert: d.Cert, Email: d.Email, CertFile: d.CertFile, KeyFile: d.KeyFile, Port: d.Port,
	}
}

func nodeDecoyOf(s agentproto.DecoySpec) model.NodeDecoy {
	return model.NodeDecoy{
		Mode: s.Mode, Template: s.Template, ProxyURL: s.ProxyURL, Domain: s.Domain,
		Cert: s.Cert, Email: s.Email, CertFile: s.CertFile, KeyFile: s.KeyFile, Port: s.Port,
	}
}

// normalizeNodeDecoy refuses a decoy an agent would not serve, with the pages of this panel's build as
// the pages it knows, and returns the decoy in the form the agent stores.
func normalizeNodeDecoy(d model.NodeDecoy) (model.NodeDecoy, error) {
	spec, err := agentproto.NormalizeDecoy(decoySpecOf(d), agentproto.DecoyRules{Templates: frontproxy.DecoyTemplateNames()})
	if err != nil {
		return model.NodeDecoy{}, common.NewError(err.Error())
	}
	return nodeDecoyOf(spec), nil
}

// decoyToStore is the column value a save leaves on a node: the decoy it names, like every other field
// of a save, and none on a node that is not an agent.
func decoyToStore(existing *model.Node, requested model.NodeDecoy) (string, error) {
	var decoy model.NodeDecoy
	if existing.Kind == model.NodeKindAgent {
		var err error
		if decoy, err = normalizeNodeDecoy(requested); err != nil {
			return "", err
		}
	}
	out, err := json.Marshal(decoy)
	return string(out), err
}

// decoyStateFrom is how an agent says its decoy stands, kept to a length the panel stores, since
// another machine's answer is not trusted to be small.
func decoyStateFrom(state agentproto.DecoyState) model.NodeDecoyState {
	return model.NodeDecoyState{
		State:        clipRunes(state.State, maxDecoyStateLen),
		Cert:         clipRunes(state.Cert, maxDecoyStateLen),
		CertNotAfter: state.CertNotAfter,
		Message:      clipRunes(state.Message, maxSidecarMessageLen),
	}
}
