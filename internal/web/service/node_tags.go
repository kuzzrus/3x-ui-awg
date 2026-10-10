package service

import (
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

const (
	maxNodeTags   = 16
	maxNodeTagLen = 32
)

// checkNodeTag refuses a tag that is too long to read in a list or holds a control character.
func checkNodeTag(tag string) error {
	if utf8.RuneCountInString(tag) > maxNodeTagLen {
		return common.NewErrorf("tag %q is longer than %d characters", tag, maxNodeTagLen)
	}
	for _, r := range tag {
		if unicode.IsControl(r) {
			return common.NewErrorf("tag %q holds a control character", tag)
		}
	}
	return nil
}

// normalizeNodeTags trims the tags and drops the empty ones and the repeats, for tags are matched
// without regard to case, and refuses a list a node cannot carry.
func normalizeNodeTags(tags []string) ([]string, error) {
	out := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if err := checkNodeTag(tag); err != nil {
			return nil, err
		}
		key := strings.ToLower(tag)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, tag)
	}
	if len(out) > maxNodeTags {
		return nil, common.NewErrorf("a node takes at most %d tags", maxNodeTags)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// tagsToStore is the column value a save leaves on a node: the tags it names, like every other
// field of a save, and none on a node that is not an agent.
func tagsToStore(existing *model.Node, requested []string) (string, error) {
	var tags []string
	if existing.Kind == model.NodeKindAgent {
		var err error
		if tags, err = normalizeNodeTags(requested); err != nil {
			return "", err
		}
	}
	out, err := json.Marshal(tags)
	return string(out), err
}

// ruleNodeTags reads the nodeTags of a routing rule; ok is false when it is not a list of tags.
func ruleNodeTags(raw any) (tags []string, ok bool) {
	list, isList := raw.([]any)
	if !isList {
		return nil, false
	}
	for _, item := range list {
		tag, isString := item.(string)
		if !isString {
			return nil, false
		}
		tag = strings.TrimSpace(tag)
		if tag == "" || checkNodeTag(tag) != nil {
			return nil, false
		}
		tags = append(tags, tag)
	}
	return tags, true
}

// sharesNodeTag reports whether a node's tags and the tags of a rule have one in common.
func sharesNodeTag(have, want []string) bool {
	for _, h := range have {
		for _, w := range want {
			if strings.EqualFold(h, w) {
				return true
			}
		}
	}
	return false
}

// AgentTags is the tags an agent node carries, which pick the routing rules it runs.
func (s *NodeService) AgentTags(id int) ([]string, error) {
	n := &model.Node{}
	if err := database.GetDB().Select("id", "tags").Where("id = ?", id).First(n).Error; err != nil {
		return nil, err
	}
	return n.Tags, nil
}
