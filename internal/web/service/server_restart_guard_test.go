package service

import (
	"strings"
	"testing"
)

// A double-click on the panel's Restart button used to queue behind
// XrayService's lock and then run a second, redundant stop-then-start.
func TestClaimRestartFromPanelRejectsConcurrentCall(t *testing.T) {
	s := &ServerService{}
	s.restartInFlight.Store(true)
	t.Cleanup(func() { s.restartInFlight.Store(false) })

	t.Run("rejects with the expected message", func(t *testing.T) {
		err := s.ClaimRestartFromPanel()
		if err == nil || strings.TrimSpace(err.Error()) != "a restart is already in progress" {
			t.Fatalf("ClaimRestartFromPanel() = %v, want %q", err, "a restart is already in progress")
		}
	})

	t.Run("does not clear the in-flight restart's flag", func(t *testing.T) {
		_ = s.ClaimRestartFromPanel()
		if !s.restartInFlight.Load() {
			t.Fatal("a rejected call cleared restartInFlight, want the running restart's flag untouched")
		}
	})
}

// A successful claim must be released after running, or a later sequential
// restart stays locked out. The restart itself is made to fail; only the flag matters.
func TestClaimThenRunClaimedRestartFromPanelReleasesTheFlag(t *testing.T) {
	setupSettingTestDB(t)
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "{ not valid json"); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	s := &ServerService{}
	if err := s.ClaimRestartFromPanel(); err != nil {
		t.Fatalf("ClaimRestartFromPanel() on an idle service = %v, want nil", err)
	}
	if !s.restartInFlight.Load() {
		t.Fatal("a successful claim left restartInFlight false")
	}

	_ = s.RunClaimedRestartFromPanel()

	if s.restartInFlight.Load() {
		t.Fatal("RunClaimedRestartFromPanel left restartInFlight true, want it released")
	}
}
