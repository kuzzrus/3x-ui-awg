package psiphon

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
)

// defaultConfig is this fork's own bundled reference psiphon.config -- see
// ensureDefaultConfig for what it is and, more importantly, what it is not.
//
//go:embed default_psiphon.config
var defaultConfig []byte

// applyForcedFields overwrites the psiphon.config keys the admin's own paste
// must never control, mirroring tor.renderTorrc's forced ClientOnly=1.
func applyForcedFields(raw map[string]any) {
	raw["LocalSocksProxyPort"] = SocksPort
	raw["DisableLocalSocksProxy"] = false
	// Only a socks outbound is ever used -- same minimal surface as internal/tor.
	raw["DisableLocalHTTPProxy"] = true
	// Loopback-only: a config copied from the Docker deployment commonly
	// carries "any", which would bind 0.0.0.0 here instead.
	raw["ListenInterface"] = ""
}

// SaveConfig validates and patches the admin-supplied psiphon.config, writing
// it to ConfigPath. Does not restart a running process -- call Manager.Restart for that.
func SaveConfig(raw []byte) error {
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("not a valid psiphon.config: %w", err)
	}
	if parsed == nil {
		return fmt.Errorf("not a valid psiphon.config: expected a JSON object")
	}
	applyForcedFields(parsed)
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("re-encoding psiphon.config: %w", err)
	}
	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", configDir(), err)
	}
	// 0600: identifies one specific credential to Psiphon Inc., same treatment as the Tor control cookie.
	return os.WriteFile(ConfigPath(), out, 0o600)
}

// EgressRegion reads back the ISO 3166-1 alpha-2 code the config requests, or "" for auto.
func EgressRegion() (string, error) {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return "", err
	}
	var parsed struct {
		EgressRegion string
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("cannot parse %s: %w", ConfigPath(), err)
	}
	return parsed.EgressRegion, nil
}

// SetEgressRegion patches just the EgressRegion field. Psiphon does not
// hot-reload it, so the caller restarts the process afterward.
func SetEgressRegion(region string) error {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return fmt.Errorf("no config to update -- upload a psiphon.config first: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("cannot parse %s: %w", ConfigPath(), err)
	}
	parsed["EgressRegion"] = region
	applyForcedFields(parsed)
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("re-encoding psiphon.config: %w", err)
	}
	return os.WriteFile(ConfigPath(), out, 0o600)
}

// ensureDefaultConfig seeds defaultConfig the first time no config exists
// yet, so Install leaves Psiphon usable without a manual upload. Never
// overwrites a config that's already there -- an admin's own upload (or a
// previous seed) always wins, exactly like any other call to SaveConfig.
//
// defaultConfig is not Psiphon Inc.'s to hand out; it's this fork's own
// PropagationChannelId/SponsorId, sourced (with the admin's own informed,
// explicit sign-off -- this is a real escalation from "one admin's manual
// upload" to "every install of this fork shares one identity with Psiphon's
// network," see the PR this shipped in) from the third-party Docker image
// the reference project (github.com/kuzzrus/psiphon-vps) runs, the same
// provenance already disclosed for the abandoned Docker-fetch approach this
// replaced. Goes through the exact same SaveConfig path as any admin-supplied
// config -- applyForcedFields patches it identically, nothing here gets a
// special case.
func ensureDefaultConfig() error {
	if IsConfigured() {
		return nil
	}
	return SaveConfig(defaultConfig)
}

// identity is the subset of a psiphon.config that names whose credentials it
// carries. Used only to answer "is this the bundled default," not a full
// config comparison -- an admin's own upload could otherwise byte-differ from
// a re-derivation of defaultConfig by formatting alone and still count.
type identity struct {
	PropagationChannelId string
	SponsorId            string
}

func readIdentity(raw []byte) (identity, error) {
	var id identity
	err := json.Unmarshal(raw, &id)
	return id, err
}

// UsesDefaultConfig reports whether the config on disk is this fork's own
// bundled default (see ensureDefaultConfig) rather than something the admin
// uploaded themselves. The settings UI surfaces this so nobody mistakes a
// shared community identity for a private one -- the whole reason this
// distinction is worth a function of its own.
func UsesDefaultConfig() (bool, error) {
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return false, err
	}
	current, err := readIdentity(raw)
	if err != nil {
		return false, fmt.Errorf("cannot parse %s: %w", ConfigPath(), err)
	}
	def, err := readIdentity(defaultConfig)
	if err != nil {
		return false, fmt.Errorf("cannot parse the bundled default config: %w", err)
	}
	return current.PropagationChannelId == def.PropagationChannelId &&
		current.SponsorId == def.SponsorId, nil
}
