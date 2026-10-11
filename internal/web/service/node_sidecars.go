package service

import (
	"encoding/json"
	"slices"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	maxSidecarStateLen   = 32
	maxSidecarMessageLen = 300
)

// normalizeNodeSidecars refuses a helper program no agent runs and drops the repeats.
func normalizeNodeSidecars(names []string) ([]string, error) {
	out, err := agentproto.NormalizeSidecars(names)
	if err != nil {
		return nil, common.NewError(err.Error())
	}
	return out, nil
}

// sidecarsToStore is the column value a save leaves on a node: the helper programs it names, like every
// other field of a save, and none on a node that is not an agent.
func sidecarsToStore(existing *model.Node, requested []string) (string, error) {
	var names []string
	if existing.Kind == model.NodeKindAgent {
		var err error
		if names, err = normalizeNodeSidecars(requested); err != nil {
			return "", err
		}
	}
	out, err := json.Marshal(names)
	return string(out), err
}

// sidecarStatesFrom is how an agent says its helper programs stand, kept to the programs the panel knows
// and to a length it stores, since another machine's answer is not trusted to be small.
func sidecarStatesFrom(reported []agentproto.SidecarState) []model.NodeSidecar {
	out := make([]model.NodeSidecar, 0, len(reported))
	for _, one := range reported {
		if !slices.Contains(agentproto.KnownSidecars, one.Name) ||
			slices.ContainsFunc(out, func(have model.NodeSidecar) bool { return have.Name == one.Name }) {
			continue
		}
		out = append(out, model.NodeSidecar{
			Name:    one.Name,
			State:   clipRunes(one.State, maxSidecarStateLen),
			Message: clipRunes(one.Message, maxSidecarMessageLen),
		})
	}
	return out
}

func clipRunes(text string, limit int) string {
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit])
	}
	return text
}
