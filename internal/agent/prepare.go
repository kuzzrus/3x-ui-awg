// Package agent runs Xray-core for a master panel: it applies the configs the
// master pushes, keeps the last good one, and reports what the core is doing.
package agent

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"path/filepath"
	"slices"

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

// forceLogPaths points the core's log files at logDir, since the master's paths belong
// to its own filesystem; empty (stdout) and "none" stay as they are.
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
		var path string
		if json.Unmarshal(section[key], &path) != nil || path == "" || path == "none" {
			continue
		}
		forced, err := json.Marshal(filepath.Join(logDir, key+".log"))
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
