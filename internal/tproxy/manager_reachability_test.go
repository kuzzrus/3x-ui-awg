package tproxy

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

const reachabilityHost = "proxy.example.com"

func aliceInstance() Instance {
	return Instance{Id: 1, Clients: []ClientSecret{{Name: "alice", Secret: "00112233445566778899aabbccddeeff"}}}
}

// seedTelegramConf replaces the conf that seedTelegramConfigFiles wrote with one whose proxy_for lines list addrs.
func seedTelegramConf(t *testing.T, addrs ...string) {
	t.Helper()
	if err := os.WriteFile(proxyMultiConfPath(), []byte(telegramConf(addrs...)), 0o600); err != nil {
		t.Fatalf("write proxy-multi.conf: %v", err)
	}
}

func TestManagerEnsureRefusesToStartWhileTelegramIsUnreachable(t *testing.T) {
	pidFile := installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	dials := recordDials(t, errors.New("no route to host"))
	var firewalled atomic.Int32
	setUp := applyFirewall
	applyFirewall = func(ctx context.Context, ports []int) error {
		firewalled.Add(1)
		return setUp(ctx, ports)
	}
	t.Cleanup(func() { applyFirewall = setUp })
	m := newTestManager()
	t.Cleanup(m.StopAll)

	err := m.Ensure(reachabilityHost, aliceInstance())
	if !errors.Is(err, errTelegramUnreachable) {
		t.Fatalf("Ensure error = %v, want it to wrap errTelegramUnreachable", err)
	}
	if got := spawnCount(t, pidFile); got != 0 {
		t.Errorf("spawned %d process(es) for a refused start, want none", got)
	}
	if got := firewalled.Load(); got != 0 {
		t.Errorf("the firewall was set up %d time(s) for a refused start, want none: it follows the check", got)
	}
	if m.IsRunning(1) {
		t.Error("the engine runs although its start was refused")
	}
	if _, ok := m.ServerAddr(); ok {
		t.Error("the shared relay runs for an engine that was refused")
	}

	// The admin repairs the route and saves again: a failure seen a moment ago must not stand in the way.
	dials.set(nil)
	if err := m.Ensure(reachabilityHost, aliceInstance()); err != nil {
		t.Fatalf("Ensure once Telegram answers: %v", err)
	}
	if !m.IsRunning(1) {
		t.Error("the engine does not run after Telegram answered")
	}
	if got := spawnCount(t, pidFile); got != 2 {
		t.Errorf("spawned %d process(es) after the repair, want 2 (the engine and the relay)", got)
	}
}

func TestManagerReconcileSaysARefusedStartOnceAndTriesAgainWhenTheVerdictIsStale(t *testing.T) {
	installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	marker := "203.0.113.50:8888"
	seedTelegramConf(t, marker)
	clock := stubClock(t)
	dials := recordDials(t, errors.New("no route to host"))
	m := newTestManager()
	t.Cleanup(m.StopAll)
	desired := []Instance{aliceInstance()}
	warned := func() int { return loggedCount("warning", marker) }
	failed := func() int { return loggedCount("warning", "reconcile failed for inbound 1") }
	w0, f0 := warned(), failed()

	for range 3 {
		if m.Reconcile(reachabilityHost, desired) {
			t.Fatal("Reconcile reported a change for a start that is refused")
		}
		clock.advance(10 * time.Second)
	}
	if got := dials.count(); got != 1 {
		t.Errorf("%d probe(s) over three ticks, want 1: a verdict is kept for a while", got)
	}
	if got := warned() - w0; got != 1 {
		t.Errorf("the refusal was said %d time(s) over three ticks, want once", got)
	}
	if got := failed() - f0; got != 0 {
		t.Errorf("Reconcile logged the refusal as its own failure %d time(s), want none: the verdict said it", got)
	}

	clock.advance(dcProbeFailTTL)
	m.Reconcile(reachabilityHost, desired)
	if got := dials.count(); got != 2 {
		t.Errorf("%d probe(s) after the failure went stale, want 2", got)
	}
	if got := warned() - w0; got != 1 {
		t.Errorf("the same verdict was said %d time(s) in all, want once", got)
	}

	dials.set(nil)
	clock.advance(dcProbeFailTTL)
	if !m.Reconcile(reachabilityHost, desired) {
		t.Fatal("Reconcile did not start the engine once Telegram answered")
	}
	if !m.IsRunning(1) {
		t.Error("the engine does not run after Telegram answered")
	}
}

func TestManagerStartIsNotGatedWhenTheEngineRoutesThroughXray(t *testing.T) {
	installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	dials := recordDials(t, errors.New("no route to host"))
	m := newTestManager()
	t.Cleanup(m.StopAll)

	// Its own dials leave through Xray, so this host's route to Telegram says nothing about them.
	inst := aliceInstance()
	inst.RouteThroughXray, inst.RouteXrayPort = true, 40000
	if err := m.Ensure(reachabilityHost, inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := dials.count(); got != 0 {
		t.Errorf("%d probe(s) for an engine that routes through Xray, want none", got)
	}
	if !m.IsRunning(1) {
		t.Error("the engine does not run")
	}
}

func TestManagerEnsureWithoutClientsDoesNotProbe(t *testing.T) {
	installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	dials := recordDials(t, errors.New("no route to host"))
	m := newTestManager()
	t.Cleanup(m.StopAll)

	// An inbound whose last client left has no engine to start, so nothing to wait for Telegram for.
	if err := m.Ensure(reachabilityHost, Instance{Id: 1}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := dials.count(); got != 0 {
		t.Errorf("%d probe(s) for an instance with no clients, want none", got)
	}
}

func TestManagerRunningEngineIsLeftAloneAndItsRestartIsNotGated(t *testing.T) {
	pidFile := installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	clock := stubClock(t)
	dials := recordDials(t, nil)
	m := newTestManager()
	t.Cleanup(m.StopAll)
	inst := aliceInstance()
	if err := m.Ensure(reachabilityHost, inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := dials.count(); got != 1 {
		t.Fatalf("%d probe(s) for the first start, want 1", got)
	}

	// Telegram is lost and the verdict has gone stale, but the engine runs: a tick has nothing to start, so nothing to look at.
	dials.set(errors.New("no route to host"))
	clock.advance(dcProbeOKTTL + time.Minute)
	before := spawnCount(t, pidFile)
	m.Reconcile(reachabilityHost, []Instance{inst})
	if got := dials.count(); got != 1 {
		t.Errorf("%d probe(s) after a tick that found the engine running, want none more", got)
	}

	// An edit of its secrets restarts it, which a lost route does not stop: it was serving, and it still is.
	inst.Clients[0].Secret = "aabbccdd00112233445566778899eeff"
	if err := m.Ensure(reachabilityHost, inst); err != nil {
		t.Fatalf("Ensure with a changed secret: %v", err)
	}
	if got := dials.count(); got != 1 {
		t.Errorf("%d probe(s) for a restart, want none more", got)
	}
	if got := spawnCount(t, pidFile) - before; got != 2 {
		t.Errorf("spawned %d process(es) for the restart, want 2 (the engine and the relay)", got)
	}
}

func TestManagerStoppedEngineStartsAgainOnlyWhenTelegramAnswers(t *testing.T) {
	installFakeBinaries(t)
	seedTelegramConfigFiles(t)
	clock := stubClock(t)
	dials := recordDials(t, nil)
	m := newTestManager()
	t.Cleanup(m.StopAll)
	desired := []Instance{aliceInstance()}
	if err := m.Ensure(reachabilityHost, desired[0]); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	// The engine dies, and by then Telegram cannot be reached either.
	m.mu.Lock()
	_ = m.mtproxies[1].proc.Stop()
	m.mu.Unlock()
	if m.IsRunning(1) {
		t.Fatal("the engine still runs after it was stopped")
	}
	dials.set(errors.New("no route to host"))
	clock.advance(dcProbeOKTTL)
	if m.Reconcile(reachabilityHost, desired) {
		t.Error("Reconcile started the engine again although Telegram cannot be reached")
	}
	if m.IsRunning(1) {
		t.Error("the engine runs again although Telegram cannot be reached")
	}

	dials.set(nil)
	clock.advance(dcProbeFailTTL)
	if !m.Reconcile(reachabilityHost, desired) {
		t.Error("Reconcile did not start the engine again once Telegram answered")
	}
	if !m.IsRunning(1) {
		t.Error("the engine does not run after Telegram answered")
	}
}
