package service

import "testing"

// A double-click (or an impatient retry while the panel looks frozen) used to
// queue behind XrayService's own lock and then run a second, fully redundant
// stop-then-start once the first one finished. RestartXrayService now rejects
// outright instead of queuing.
func TestRestartXrayServiceRejectsConcurrentCall(t *testing.T) {
	s := &ServerService{}
	s.restartInFlight.Store(true)
	t.Cleanup(func() { s.restartInFlight.Store(false) })

	if err := s.RestartXrayService(); err == nil {
		t.Fatal("RestartXrayService succeeded while a restart was already marked in-flight, want a rejection")
	}
}
