package service

import (
	"testing"
)

func TestRestartXrayRespectsManualStop(t *testing.T) {
	setupSettingTestDB(t)
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "{ not valid json"); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	t.Cleanup(func() { isManuallyStopped.Store(false) })

	isManuallyStopped.Store(true)
	_ = (&XrayService{}).RestartXray(false)

	if !isManuallyStopped.Load() {
		t.Fatal("a non-forced restart cleared a deliberate manual stop and would revive xray")
	}
}

func TestApplyPendingRestartReArmsFlagOnFailure(t *testing.T) {
	setupSettingTestDB(t)
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", "{ not valid json"); err != nil {
		t.Fatalf("seed template: %v", err)
	}
	t.Cleanup(func() {
		isManuallyStopped.Store(false)
		isNeedXrayRestart.Store(false)
	})
	isManuallyStopped.Store(false)

	svc := &XrayService{}
	svc.SetToNeedRestart()
	svc.ApplyPendingRestart()

	if !isNeedXrayRestart.Load() {
		t.Fatal("a failed restart must re-arm the need-restart flag so the pending config change is retried")
	}
}

func stopAmneziawgRelayResyncTimer() {
	amneziawgRelayResyncMu.Lock()
	defer amneziawgRelayResyncMu.Unlock()
	if amneziawgRelayResyncTimer != nil {
		amneziawgRelayResyncTimer.Stop()
		amneziawgRelayResyncTimer = nil
	}
}

func TestScheduleAmneziaWGRelayResyncSetsNeedRestartFlagImmediately(t *testing.T) {
	t.Cleanup(func() {
		isNeedXrayRestart.Store(false)
		stopAmneziawgRelayResyncTimer()
	})
	isNeedXrayRestart.Store(false)

	(&XrayService{}).ScheduleAmneziaWGRelayResync()

	// The flag must be set synchronously, before the debounce timer has any
	// chance to fire -- a caller checking IsNeedRestartAndSetFalse right
	// after this call must already see the pending change.
	if !isNeedXrayRestart.Load() {
		t.Fatal("expected the need-restart flag to be set immediately, not only once the debounce timer fires")
	}
}

// TestScheduleAmneziaWGRelayResyncCoalescesRapidCalls proves a second call
// arms a fresh timer and cancels the first, rather than leaving both live --
// two pending timers would mean two restarts scheduled for one burst of
// edits instead of the intended single debounced restart. Uses
// (*time.Timer).Stop's own return value (false once a timer has already
// fired or been stopped) as the observable signal, so this needs no real
// sleep and cannot flake on timing.
func TestScheduleAmneziaWGRelayResyncCoalescesRapidCalls(t *testing.T) {
	t.Cleanup(func() {
		isNeedXrayRestart.Store(false)
		stopAmneziawgRelayResyncTimer()
	})

	svc := &XrayService{}
	svc.ScheduleAmneziaWGRelayResync()

	amneziawgRelayResyncMu.Lock()
	first := amneziawgRelayResyncTimer
	amneziawgRelayResyncMu.Unlock()
	if first == nil {
		t.Fatal("expected a debounce timer to be armed")
	}

	svc.ScheduleAmneziaWGRelayResync()

	amneziawgRelayResyncMu.Lock()
	second := amneziawgRelayResyncTimer
	amneziawgRelayResyncMu.Unlock()
	if second == nil {
		t.Fatal("expected a debounce timer to still be armed after the second call")
	}
	if second == first {
		t.Fatal("a second call must arm a fresh timer, not reuse the first")
	}
	if first.Stop() {
		t.Fatal("the first call's timer should already have been stopped by the second call, not left pending")
	}
}
