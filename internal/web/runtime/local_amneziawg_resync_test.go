package runtime

import (
	"context"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// Both tests below assert that the AmneziaWG paths call the fast-resync
// hook instead of the general SetNeedRestart -- see
// XrayService.ScheduleAmneziaWGRelayResync for why the two must stay
// separate. SetNeedRestart is wired to fail the test if it's ever reached
// from these paths, so a regression back to the slow hook is caught here
// rather than only by a live ~30s "connection refused" repro.

func TestUpdateAmneziaWGInboundSchedulesFastResync(t *testing.T) {
	calls := 0
	l := NewLocal(LocalDeps{
		APIPort:                      func() int { return 0 },
		SetNeedRestart:               func() { t.Fatal("AmneziaWG update must use ScheduleAmneziaWGRelayResync, not the slow SetNeedRestart") },
		ScheduleAmneziaWGRelayResync: func() { calls++ },
	})

	// newIb.Enable=false keeps this on the cheap amneziawgnet.Manager.Remove
	// path (a no-op on an id the manager never Ensure'd) rather than the
	// heavy Ensure path, which needs a real embedded device and isn't
	// exercised by any test yet.
	oldIb := &model.Inbound{Id: 1, Protocol: model.AmneziaWG, Enable: false}
	newIb := &model.Inbound{Id: 1, Protocol: model.AmneziaWG, Enable: false}
	if err := l.UpdateInbound(context.Background(), oldIb, newIb); err != nil {
		t.Fatalf("UpdateInbound: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected ScheduleAmneziaWGRelayResync to be called exactly once, got %d", calls)
	}
}

func TestDelAmneziaWGInboundSchedulesFastResync(t *testing.T) {
	calls := 0
	l := NewLocal(LocalDeps{
		APIPort:                      func() int { return 0 },
		SetNeedRestart:               func() { t.Fatal("AmneziaWG delete must use ScheduleAmneziaWGRelayResync, not the slow SetNeedRestart") },
		ScheduleAmneziaWGRelayResync: func() { calls++ },
	})

	ib := &model.Inbound{Id: 2, Protocol: model.AmneziaWG, Enable: true}
	if err := l.DelInbound(context.Background(), ib); err != nil {
		t.Fatalf("DelInbound: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected ScheduleAmneziaWGRelayResync to be called exactly once, got %d", calls)
	}
}
