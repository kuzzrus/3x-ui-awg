package xray

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// ApplyHot reconciles the running core with newCfg over its gRPC API; false means
// the caller must restart, which also cleans up whatever a failed apply left behind.
func ApplyHot(process *Process, newCfg *Config, restartToDropUsers func(*HotDiff) bool) bool {
	oldCfg := process.GetConfig()
	diff, ok := ComputeHotDiff(oldCfg, newCfg)
	if !ok {
		logger.Debug("hot apply: config change is not API-applicable, falling back to restart")
		return false
	}
	if diff.Empty() {
		process.SetConfig(newCfg)
		return true
	}
	// The core's RemoveUser drops the credential only, so a disabled or deleted
	// client needs the restart this setting asks for.
	if restartToDropUsers != nil && restartToDropUsers(diff) {
		logger.Info("hot apply: clients left the config, restarting to drop their live sessions")
		return false
	}

	apiPort := process.GetAPIPort()
	if apiPort <= 0 {
		return false
	}
	// A dedicated client: a shared one may be mid-poll on another goroutine and is
	// reset around restarts.
	hotAPI := XrayAPI{}
	if err := hotAPI.Init(apiPort); err != nil {
		logger.Debug("hot apply: failed to init xray api:", err)
		return false
	}
	defer hotAPI.Close()

	// Removals first so changed handlers and port swaps never collide with
	// the additions that follow.
	for _, u := range diff.RemovedUsers {
		if err := hotAPI.RemoveUser(u.Tag, u.Email); err != nil && !IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove user [", u.Email, "] from [", u.Tag, "] failed:", err)
			return false
		}
	}
	for _, tag := range diff.RemovedInboundTags {
		if err := hotAPI.DelInbound(tag); err != nil && !IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove inbound [", tag, "] failed:", err)
			return false
		}
	}
	for _, tag := range diff.RemovedOutboundTags {
		if err := hotAPI.DelOutbound(tag); err != nil && !IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove outbound [", tag, "] failed:", err)
			return false
		}
	}
	for _, ob := range diff.AddedOutbounds {
		if err := addOutboundReconciling(&hotAPI, ob); err != nil {
			logger.Info("hot apply: add outbound failed:", err)
			return false
		}
	}
	for _, ib := range diff.AddedInbounds {
		if err := addInboundReconciling(&hotAPI, ib); err != nil {
			logger.Info("hot apply: add inbound failed:", err)
			return false
		}
	}
	for _, u := range diff.AddedUsers {
		if err := addUserReconciling(&hotAPI, u); err != nil {
			logger.Info("hot apply: add user [", u.Email, "] to [", u.Tag, "] failed:", err)
			return false
		}
	}
	if diff.RoutingConfig != nil {
		if err := hotAPI.ApplyRoutingConfig(diff.RoutingConfig); err != nil {
			logger.Info("hot apply: apply routing config failed:", err)
			return false
		}
	}

	process.SetConfig(newCfg)
	return true
}

// addUserReconciling adds a user, and on an email conflict (the user was
// already applied through the runtime API) replaces the existing user instead.
func addUserReconciling(api *XrayAPI, u UserOp) error {
	err := api.AddUser(u.Protocol, u.Tag, u.User)
	if err == nil || !IsUserExistsErr(err) {
		return err
	}
	if delErr := api.RemoveUser(u.Tag, u.Email); delErr != nil && !IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddUser(u.Protocol, u.Tag, u.User)
}

// addInboundReconciling adds an inbound, replacing the handler on a tag conflict
// (created through the runtime API while the stored snapshot was stale).
func addInboundReconciling(api *XrayAPI, inbound []byte) error {
	err := api.AddInbound(inbound)
	if err == nil || !IsExistingTagErr(err) {
		return err
	}
	var meta struct {
		Tag string `json:"tag"`
	}
	if jsonErr := json.Unmarshal(inbound, &meta); jsonErr != nil || meta.Tag == "" {
		return err
	}
	if delErr := api.DelInbound(meta.Tag); delErr != nil && !IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddInbound(inbound)
}

// addOutboundReconciling mirrors addInboundReconciling for outbounds.
func addOutboundReconciling(api *XrayAPI, outbound []byte) error {
	err := api.AddOutbound(outbound)
	if err == nil || !IsExistingTagErr(err) {
		return err
	}
	var meta struct {
		Tag string `json:"tag"`
	}
	if jsonErr := json.Unmarshal(outbound, &meta); jsonErr != nil || meta.Tag == "" {
		return err
	}
	if delErr := api.DelOutbound(meta.Tag); delErr != nil && !IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddOutbound(outbound)
}
