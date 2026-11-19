package portguard

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeStore keeps the State in memory and notes what happens to it.
type fakeStore struct {
	state   State
	saved   int
	loadErr error
	saveErr error
	// events is shared with the fake firewall, so that a test can tell what came first.
	events *[]string
}

func (s *fakeStore) Load() (State, error) {
	if s.loadErr != nil {
		return State{}, s.loadErr
	}
	return s.state, nil
}

func (s *fakeStore) Save(st State) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.state = st
	s.saved++
	if s.events != nil {
		*s.events = append(*s.events, "save")
	}
	return nil
}

// fakeFirewall remembers what it was asked to close.
type fakeFirewall struct {
	closed    []Port
	applied   int
	removed   int
	applyErr  error
	removeErr error
	events    *[]string
}

func (f *fakeFirewall) Apply(_ context.Context, ports []Port) error {
	if f.events != nil {
		*f.events = append(*f.events, "apply")
	}
	if f.applyErr != nil {
		return f.applyErr
	}
	f.applied++
	f.closed = slices.Clone(ports)
	return nil
}

func (f *fakeFirewall) Remove(context.Context) error {
	if f.events != nil {
		*f.events = append(*f.events, "remove")
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	f.removed++
	f.closed = nil
	return nil
}

type guardRig struct {
	m      *Manager
	store  *fakeStore
	fw     *fakeFirewall
	now    time.Time
	events []string
}

func newGuardRig(protected ...Port) *guardRig {
	r := &guardRig{store: &fakeStore{}, fw: &fakeFirewall{}, now: time.Unix(1_800_000_000, 0)}
	r.store.events, r.fw.events = &r.events, &r.events
	r.m = New(r.store, r.fw, func() []Port { return protected })
	r.m.now = func() time.Time { return r.now }
	return r
}

func ports(proto Proto, numbers ...int) []Port {
	out := make([]Port, len(numbers))
	for i, n := range numbers {
		out[i] = Port{Number: n, Proto: proto}
	}
	return out
}

func TestPlanSaysWhatWouldBeClosedAndOpened(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443)}
	desired := []Port{{9000, TCP}, {8443, TCP}, {9000, TCP}, {51820, UDP}}
	plan, err := r.m.Plan(desired)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if want := []Port{{8443, TCP}, {9000, TCP}, {51820, UDP}}; !slices.Equal(plan.Closed, want) {
		t.Errorf("Closed = %v, want %v: sorted, each once", plan.Closed, want)
	}
	if want := []Port{{9000, TCP}, {51820, UDP}}; !slices.Equal(plan.Close, want) {
		t.Errorf("Close = %v, want %v", plan.Close, want)
	}
	if want := ports(TCP, 2053); !slices.Equal(plan.Open, want) {
		t.Errorf("Open = %v, want %v", plan.Open, want)
	}
	if !plan.Lease {
		t.Error("Lease = false for a change that closes ports")
	}
	if plan.WindowSeconds != 120 {
		t.Errorf("WindowSeconds = %d, want 120", plan.WindowSeconds)
	}
	if !strings.Contains(plan.Ruleset, "tcp dport { 8443, 9000 } drop") || !strings.Contains(plan.Ruleset, "udp dport { 51820 } drop") {
		t.Errorf("Ruleset does not close the ports:\n%s", plan.Ruleset)
	}
	if r.fw.applied != 0 || r.fw.removed != 0 || r.store.saved != 0 {
		t.Error("Plan touched the firewall or the state")
	}
}

func TestPlanNeedsNoLeaseWhenItOnlyOpensPorts(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443)}
	plan, err := r.m.Plan(ports(TCP, 8443))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Lease {
		t.Error("Lease = true for a change that only opens a port")
	}
	if len(plan.Close) != 0 {
		t.Errorf("Close = %v, want none", plan.Close)
	}
}

// What is closed but not confirmed yet is not what the admin has agreed to, so a port in it is still a change to confirm.
func TestPlanMeasuresALeaseAgainstWhatWasConfirmed(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	plan, err := r.m.Plan(ports(TCP, 2053, 8443))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Lease {
		t.Error("Lease = false: 8443 was not confirmed")
	}
	if len(plan.Close) != 0 {
		t.Errorf("Close = %v, want none: 8443 is already closed", plan.Close)
	}
}

func TestPlanRefusesProtectedAndInvalidPorts(t *testing.T) {
	r := newGuardRig(Port{22, TCP}, Port{443, TCP})
	plan, err := r.m.Plan([]Port{{22, TCP}, {22, UDP}, {0, TCP}, {70000, TCP}, {80, "icmp"}, {443, TCP}, {8080, TCP}})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if want := []Port{{8080, TCP}, {22, UDP}}; !slices.Equal(plan.Closed, want) {
		t.Errorf("Closed = %v, want %v: 22/udp is not the SSH port", plan.Closed, want)
	}
	refused := map[Port]string{}
	for _, ref := range plan.Refused {
		refused[ref.Port] = ref.Reason
	}
	for _, p := range []Port{{22, TCP}, {443, TCP}} {
		if !strings.Contains(refused[p], "able to reach the host") {
			t.Errorf("%v: reason %q, want it to say the host would be out of reach", p, refused[p])
		}
	}
	for _, p := range []Port{{0, TCP}, {70000, TCP}, {80, "icmp"}} {
		if refused[p] != "not a port" {
			t.Errorf("%v: reason %q, want \"not a port\"", p, refused[p])
		}
	}
	if _, ok := refused[Port{22, UDP}]; ok {
		t.Error("22/udp was refused: only 22/tcp is the SSH port")
	}
}

func TestApplyClosesThePortsAndStartsALease(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053)}
	plan, err := r.m.Apply(context.Background(), []Port{{8443, TCP}, {2053, TCP}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if want := ports(TCP, 2053, 8443); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", r.fw.closed, want)
	}
	st := r.store.state
	if !slices.Equal(st.Closed, r.fw.closed) {
		t.Errorf("State.Closed = %v, want what the firewall closes", st.Closed)
	}
	if st.Pending == nil {
		t.Fatal("no lease was started for a change that closes 8443")
	}
	if want := r.now.Add(LeaseWindow); !st.Pending.Deadline.Equal(want) {
		t.Errorf("Deadline = %v, want %v", st.Pending.Deadline, want)
	}
	if want := ports(TCP, 2053); !slices.Equal(st.Pending.Previous, want) {
		t.Errorf("Previous = %v, want %v", st.Pending.Previous, want)
	}
	if !plan.Lease {
		t.Error("the plan says there is no lease")
	}
}

func TestApplyOfNoPortsRemovesTheRules(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053)}
	if _, err := r.m.Apply(context.Background(), nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.fw.removed != 1 || r.fw.applied != 0 {
		t.Errorf("removed %d, applied %d, want the rules removed once and not applied", r.fw.removed, r.fw.applied)
	}
	if st := r.store.state; len(st.Closed) != 0 || st.Pending != nil {
		t.Errorf("State = %+v, want nothing closed and nothing pending: opening needs no confirmation", st)
	}
}

func TestApplyThatOnlyOpensPortsHasNoLeaseAndEndsALeaseThatWaits(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	if _, err := r.m.Apply(context.Background(), ports(TCP, 2053)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if r.store.state.Pending != nil {
		t.Error("a lease is still pending after the change went back to what was confirmed")
	}
	if want := ports(TCP, 2053); !slices.Equal(r.store.state.Closed, want) {
		t.Errorf("Closed = %v, want %v", r.store.state.Closed, want)
	}
}

// Applying again while a change waits must not turn the unconfirmed ports into confirmed ones.
func TestApplyAgainKeepsWhatWasConfirmedAndRestartsTheClock(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(10 * time.Second), Previous: ports(TCP, 2053)}}
	r.now = r.now.Add(5 * time.Second)
	if _, err := r.m.Apply(context.Background(), ports(TCP, 8443, 9000)); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	p := r.store.state.Pending
	if p == nil {
		t.Fatal("no lease after the second change")
	}
	if want := ports(TCP, 2053); !slices.Equal(p.Previous, want) {
		t.Errorf("Previous = %v, want %v: the confirmed ports, not the ones that waited", p.Previous, want)
	}
	if want := r.now.Add(LeaseWindow); !p.Deadline.Equal(want) {
		t.Errorf("Deadline = %v, want %v", p.Deadline, want)
	}
}

func TestApplyRefusesProtectedPortsAndChangesNothing(t *testing.T) {
	r := newGuardRig(Port{22, TCP})
	r.store.state = State{Closed: ports(TCP, 2053)}
	plan, err := r.m.Apply(context.Background(), []Port{{22, TCP}, {8443, TCP}})
	if !errors.Is(err, ErrRefused) {
		t.Fatalf("Apply error = %v, want ErrRefused", err)
	}
	if !strings.Contains(err.Error(), "22/tcp") {
		t.Errorf("error = %q, want it to name 22/tcp", err)
	}
	if len(plan.Refused) != 1 {
		t.Errorf("the plan refuses %d port(s), want 1", len(plan.Refused))
	}
	if r.fw.applied != 0 || r.fw.removed != 0 || r.store.saved != 0 {
		t.Error("a refused change touched the firewall or the state")
	}
}

func TestApplyChangesNoStateWhenTheFirewallRefuses(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053)}
	r.fw.applyErr = errors.New("nft: no luck")
	if _, err := r.m.Apply(context.Background(), ports(TCP, 8443)); err == nil {
		t.Fatal("Apply succeeded although the firewall refused")
	}
	if r.store.saved != 0 {
		t.Error("the state was saved for a change that was not made")
	}
}

func TestApplyPutsTheOldRulesBackWhenTheStateCannotBeSaved(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053)}
	r.fw.closed = ports(TCP, 2053)
	r.store.saveErr = errors.New("database is locked")
	if _, err := r.m.Apply(context.Background(), ports(TCP, 8443)); err == nil {
		t.Fatal("Apply succeeded although the state could not be saved")
	}
	if want := ports(TCP, 2053); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v: rules that nothing remembers cannot be rolled back", r.fw.closed, want)
	}
}

func TestConfirmKeepsTheChange(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute)}}
	if err := r.m.Confirm(); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if r.store.state.Pending != nil {
		t.Error("the change still waits after it was confirmed")
	}
	if want := ports(TCP, 8443); !slices.Equal(r.store.state.Closed, want) {
		t.Errorf("Closed = %v, want %v", r.store.state.Closed, want)
	}
	if r.fw.applied != 0 || r.fw.removed != 0 {
		t.Error("Confirm touched the firewall")
	}
}

func TestConfirmAndRollbackWithNothingPendingSayNoChangeWaits(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443)}
	if err := r.m.Confirm(); !errors.Is(err, ErrNothingPending) {
		t.Errorf("Confirm error = %v, want ErrNothingPending", err)
	}
	if err := r.m.Rollback(context.Background()); !errors.Is(err, ErrNothingPending) {
		t.Errorf("Rollback error = %v, want ErrNothingPending", err)
	}
	if r.fw.applied != 0 || r.fw.removed != 0 {
		t.Error("the firewall was touched with nothing pending")
	}
}

func TestRollbackPutsBackWhatWasClosedBefore(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	if err := r.m.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if want := ports(TCP, 2053); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", r.fw.closed, want)
	}
	if st := r.store.state; st.Pending != nil || !slices.Equal(st.Closed, ports(TCP, 2053)) {
		t.Errorf("State = %+v, want 2053 closed and nothing pending", st)
	}
}

func TestRollbackToNoPortsRemovesTheRules(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute)}}
	if err := r.m.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if r.fw.removed != 1 || r.fw.applied != 0 {
		t.Errorf("removed %d, applied %d, want the rules removed once", r.fw.removed, r.fw.applied)
	}
}

// A rollback that fails must not be tried again at every tick for ever, so the pending change is forgotten first.
func TestRollbackForgetsThePendingChangeBeforeUndoingIt(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	if err := r.m.Rollback(context.Background()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if len(r.events) < 2 || r.events[0] != "save" || r.events[1] != "apply" {
		t.Errorf("events = %v, want the state saved before the rules are put back", r.events)
	}
}

func TestRollbackRemovesTheRulesWhenTheEarlierOnesCannotBePutBack(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	r.fw.applyErr = errors.New("nft: no luck")
	err := r.m.Rollback(context.Background())
	if err == nil {
		t.Fatal("Rollback reported nothing wrong although the earlier rules were not put back")
	}
	if r.fw.removed != 1 {
		t.Errorf("the rules were removed %d time(s), want 1: an admin who cannot get in is the worse case", r.fw.removed)
	}
	if st := r.store.state; len(st.Closed) != 0 || st.Pending != nil {
		t.Errorf("State = %+v, want nothing closed and nothing pending", st)
	}
}

func TestRollbackStillReportsWhenNothingCouldBeUndone(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}}
	r.fw.applyErr = errors.New("nft: no luck")
	r.fw.removeErr = errors.New("nft: no luck either")
	err := r.m.Rollback(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no luck either") {
		t.Errorf("Rollback error = %v, want it to say the rules could not be removed either", err)
	}
}

func TestTickRollsBackOnlyWhenTheDeadlineHasCome(t *testing.T) {
	r := newGuardRig()
	deadline := r.now.Add(LeaseWindow)
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: deadline, Previous: ports(TCP, 2053)}}

	r.now = deadline.Add(-time.Second)
	r.m.Tick(context.Background())
	if r.store.state.Pending == nil || r.fw.applied != 0 || r.fw.removed != 0 {
		t.Fatal("Tick rolled back a change that still had a second to be confirmed")
	}

	r.now = deadline
	r.m.Tick(context.Background())
	if r.store.state.Pending != nil {
		t.Error("the change still waits at its deadline")
	}
	if want := ports(TCP, 2053); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", r.fw.closed, want)
	}
}

func TestTickDoesNothingWithNothingPending(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443)}
	r.m.Tick(context.Background())
	if r.fw.applied != 0 || r.fw.removed != 0 || r.store.saved != 0 {
		t.Error("Tick changed something with nothing pending")
	}
}

func TestTickSurvivesAnUnreadableState(t *testing.T) {
	r := newGuardRig()
	r.store.loadErr = errors.New("database is locked")
	r.m.Tick(context.Background())
	if r.fw.applied != 0 || r.fw.removed != 0 {
		t.Error("Tick touched the firewall without a state")
	}
}

func TestRestorePutsTheClosedPortsBack(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 2053, 8443)}
	r.m.Restore(context.Background())
	if want := ports(TCP, 2053, 8443); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", r.fw.closed, want)
	}
}

func TestRestoreWithNothingClosedLeavesTheFirewallAlone(t *testing.T) {
	r := newGuardRig()
	r.m.Restore(context.Background())
	if r.fw.applied != 0 || r.fw.removed != 0 {
		t.Error("Restore touched the firewall with nothing closed")
	}
}

func TestRestoreKeepsAChangeThatStillHasTimeToBeConfirmed(t *testing.T) {
	r := newGuardRig()
	pending := &Pending{Deadline: r.now.Add(time.Minute), Previous: ports(TCP, 2053)}
	r.store.state = State{Closed: ports(TCP, 8443), Pending: pending}
	r.m.Restore(context.Background())
	if want := ports(TCP, 8443); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want the pending ports %v", r.fw.closed, want)
	}
	if r.store.state.Pending != pending {
		t.Error("Restore changed the pending change, whose clock goes on")
	}
}

func TestRestoreRollsBackAChangeThatWasNotConfirmedBeforeTheStop(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443), Pending: &Pending{Deadline: r.now.Add(-time.Second), Previous: ports(TCP, 2053)}}
	r.m.Restore(context.Background())
	if want := ports(TCP, 2053); !slices.Equal(r.fw.closed, want) {
		t.Errorf("the firewall closes %v, want %v", r.fw.closed, want)
	}
	if r.store.state.Pending != nil {
		t.Error("the change still waits after it was rolled back")
	}
}

func TestRestoreSurvivesAFirewallThatRefuses(t *testing.T) {
	r := newGuardRig()
	r.store.state = State{Closed: ports(TCP, 8443)}
	r.fw.applyErr = errors.New("nft was not found")
	r.m.Restore(context.Background())
	if want := ports(TCP, 8443); !slices.Equal(r.store.state.Closed, want) {
		t.Errorf("State.Closed = %v, want %v: a host without nft today may have it tomorrow", r.store.state.Closed, want)
	}
}

func TestStateAndPlanSurfaceAStoreThatFails(t *testing.T) {
	r := newGuardRig()
	r.store.loadErr = errors.New("database is locked")
	if _, err := r.m.State(); err == nil {
		t.Error("State hid the store's error")
	}
	if _, err := r.m.Plan(ports(TCP, 8443)); err == nil {
		t.Error("Plan hid the store's error")
	}
	if _, err := r.m.Apply(context.Background(), ports(TCP, 8443)); err == nil {
		t.Error("Apply hid the store's error")
	}
	if err := r.m.Confirm(); err == nil {
		t.Error("Confirm hid the store's error")
	}
	if err := r.m.Rollback(context.Background()); err == nil {
		t.Error("Rollback hid the store's error")
	}
}
