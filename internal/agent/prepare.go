// Package agent runs Xray-core for a master panel: it applies the configs the
// master pushes, keeps the last good one, and reports what the core is doing.
package agent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const apiTag = "api"

var apiLoopback = netip.MustParseAddr("127.0.0.1")

// ConfigError is a config the agent refuses, as opposed to a failure of the agent itself.
type ConfigError struct{ Reason string }

func (e *ConfigError) Error() string { return e.Reason }

func configErrorf(format string, args ...any) error {
	return &ConfigError{Reason: fmt.Sprintf(format, args...)}
}

// stringList reads what Xray accepts for a tag list: one string or an array.
type stringList []string

func (l *stringList) UnmarshalJSON(raw []byte) error {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		*l = stringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return err
	}
	*l = many
	return nil
}

type routingRule struct {
	InboundTag  stringList `json:"inboundTag"`
	OutboundTag string     `json:"outboundTag"`
}

// prepareConfig parses a pushed config, checks that the agent can reach its own core
// through it, and points the core's log files into the agent's log folder.
func prepareConfig(body []byte, logDir string) (*xray.Config, error) {
	var cfg xray.Config
	if err := json.Unmarshal(body, &cfg); err != nil {
		return nil, configErrorf("config is not a valid Xray config: %v", err)
	}
	if err := requireAgentAccess(&cfg); err != nil {
		return nil, err
	}
	logSection, err := forceLogPaths(cfg.LogConfig, logDir)
	if err != nil {
		return nil, err
	}
	cfg.LogConfig = logSection
	return &cfg, nil
}

// requireAgentAccess rejects a config the agent could not drive, or whose api inbound
// would expose the core's unauthenticated API beyond loopback.
func requireAgentAccess(cfg *xray.Config) error {
	api := slices.IndexFunc(cfg.InboundConfigs, func(in xray.InboundConfig) bool { return in.Tag == apiTag })
	if api < 0 || cfg.InboundConfigs[api].Port < 1 || cfg.InboundConfigs[api].Port > 65535 {
		return configErrorf(`config has no inbound tagged "api" with a port, which the agent needs to reach its core`)
	}
	var listen string
	_ = json.Unmarshal(cfg.InboundConfigs[api].Listen, &listen)
	if addr, err := netip.ParseAddr(listen); err != nil || addr != apiLoopback {
		return configErrorf(`the "api" inbound must listen on 127.0.0.1, got %q`, listen)
	}
	if len(cfg.Stats) == 0 || string(cfg.Stats) == "null" {
		return configErrorf(`config has no "stats" section, so no traffic would be counted`)
	}
	// The section only creates the counter store; the policy decides what is counted. Client
	// usage is what quotas run on, so it is required. Inbound counters are display only.
	var policy struct {
		Levels map[string]struct {
			StatsUserUplink   bool `json:"statsUserUplink"`
			StatsUserDownlink bool `json:"statsUserDownlink"`
		} `json:"levels"`
	}
	_ = json.Unmarshal(cfg.Policy, &policy)
	if level := policy.Levels["0"]; !level.StatsUserUplink || !level.StatsUserDownlink {
		return configErrorf(`the "policy" must turn on statsUserUplink and statsUserDownlink for level 0, or no client traffic would be counted`)
	}

	var apiSection struct {
		Tag      string   `json:"tag"`
		Services []string `json:"services"`
	}
	if err := json.Unmarshal(cfg.API, &apiSection); err != nil || apiSection.Tag != apiTag {
		return configErrorf(`config has no "api" section tagged "api"`)
	}
	for _, service := range []string{"HandlerService", "StatsService"} {
		if !slices.Contains(apiSection.Services, service) {
			return configErrorf(`the "api" section must enable %s`, service)
		}
	}

	var routing struct {
		Rules []routingRule `json:"rules"`
	}
	_ = json.Unmarshal(cfg.RouterConfig, &routing)
	routed := slices.ContainsFunc(routing.Rules, func(rule routingRule) bool {
		return rule.OutboundTag == apiTag && slices.Contains(rule.InboundTag, apiTag)
	})
	if !routed {
		return configErrorf(`routing has no rule sending the "api" inbound to the "api" outbound`)
	}
	return nil
}

// forceLogPaths confines the core's log files to logDir the way the master confines its
// own: key case variants fold into one, empty and "none" stay off, any other value keeps
// its file name but loses the master's folder.
func forceLogPaths(raw json_util.RawMessage, logDir string) (json_util.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return raw, nil
	}
	var section map[string]json.RawMessage
	if err := json.Unmarshal(raw, &section); err != nil {
		return nil, configErrorf(`the "log" section is not an object: %v`, err)
	}
	changed := false
	for _, key := range []string{"access", "error"} {
		for _, variant := range caseVariants(section, key) {
			if _, has := section[key]; !has {
				section[key] = section[variant]
			}
			delete(section, variant)
			changed = true
		}
		var value string
		if json.Unmarshal(section[key], &value) != nil {
			continue
		}
		if off := strings.TrimSpace(value); off == "" || strings.EqualFold(off, "none") {
			continue
		}
		forced, err := json.Marshal(confinedLogPath(logDir, key, value))
		if err != nil {
			return nil, err
		}
		if string(forced) != string(section[key]) {
			section[key] = forced
			changed = true
		}
	}
	if !changed {
		return raw, nil
	}
	return json.Marshal(section)
}

// caseVariants lists the keys that equal want ignoring case without being want, lowest
// first: xray-core matches this section's field names case-insensitively, so each would
// reach the same setting.
func caseVariants(section map[string]json.RawMessage, want string) []string {
	var keys []string
	for key := range section {
		if key != want && strings.EqualFold(key, want) {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

func confinedLogPath(logDir, key, value string) string {
	base := path.Base(filepath.ToSlash(strings.TrimSpace(value)))
	if base == "" || base == "." || base == ".." || base == "/" {
		base = key + ".log"
	}
	return filepath.Join(logDir, base)
}
