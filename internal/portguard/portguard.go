// Package portguard closes ports of the host in the firewall, with a lease: a change that closes a port waits for the admin to
// confirm it, and is rolled back if nobody does, so that closing the port the admin reaches the panel on cannot lock them out.
package portguard

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Proto is the transport a closed port is closed for.
type Proto string

const (
	TCP Proto = "tcp"
	UDP Proto = "udp"
)

// LeaseWindow is how long a change that closes ports waits to be confirmed.
const LeaseWindow = 2 * time.Minute

// Port is one port of one transport.
type Port struct {
	Number int   `json:"port"`
	Proto  Proto `json:"proto"`
}

func (p Port) String() string { return fmt.Sprintf("%d/%s", p.Number, p.Proto) }

func (p Port) valid() bool {
	return p.Number >= 1 && p.Number <= 65535 && (p.Proto == TCP || p.Proto == UDP)
}

// Pending is a change of the closed ports that has not been confirmed: Previous is what a rollback puts back.
type Pending struct {
	Deadline time.Time `json:"deadline"`
	Previous []Port    `json:"previous"`
}

// State is all that outlives a restart of the panel: the ports that are closed, and the change that waits to be confirmed.
type State struct {
	Closed  []Port   `json:"closed"`
	Pending *Pending `json:"pending,omitempty"`
}

// Store keeps the State.
type Store interface {
	Load() (State, error)
	Save(State) error
}

// Firewall puts rules in the kernel: Apply makes exactly ports closed (at least one), Remove makes none.
type Firewall interface {
	Apply(ctx context.Context, ports []Port) error
	Remove(ctx context.Context) error
}

// Refusal is a port that was asked for and cannot be closed.
type Refusal struct {
	Port   Port   `json:"port"`
	Reason string `json:"reason"`
}

// Plan is what Apply would do, so that the admin sees it before it is done.
type Plan struct {
	Closed  []Port    `json:"closed"`
	Close   []Port    `json:"close"`
	Open    []Port    `json:"open"`
	Refused []Refusal `json:"refused"`
	// Lease is true when the change closes a port that is not closed in the confirmed state: it then waits to be confirmed.
	Lease         bool   `json:"lease"`
	WindowSeconds int    `json:"windowSeconds"`
	Ruleset       string `json:"ruleset"`
}

// ErrRefused wraps the error of an Apply that asked for ports that cannot be closed.
var ErrRefused = errors.New("portguard: some ports cannot be closed")

// ErrNothingPending is the error of a Confirm or Rollback when no change waits to be confirmed.
var ErrNothingPending = errors.New("portguard: no change waits to be confirmed")

// Manager applies the closed ports and keeps the lease.
type Manager struct {
	mu        sync.Mutex
	store     Store
	fw        Firewall
	now       func() time.Time
	protected func() []Port
}

// New returns a Manager; protected lists the ports that must never be closed (it is asked again on every plan).
func New(store Store, fw Firewall, protected func() []Port) *Manager {
	return &Manager{store: store, fw: fw, now: time.Now, protected: protected}
}

// Protected lists the ports that cannot be closed.
func (m *Manager) Protected() []Port {
	if m.protected == nil {
		return []Port{}
	}
	return slices.Clone(m.protected())
}

// State returns what is closed and what waits to be confirmed.
func (m *Manager) State() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.store.Load()
}

// Plan works out what Apply(desired) would do, and touches nothing.
func (m *Manager) Plan(desired []Port) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		return Plan{}, err
	}
	return m.planLocked(st, desired), nil
}

func (m *Manager) planLocked(st State, desired []Port) Plan {
	kept, refused := m.screen(desired)
	baseline := confirmed(st)
	// Lists, not nulls, in the JSON that the page reads.
	p := Plan{Closed: kept, Close: []Port{}, Open: []Port{}, Refused: refused, WindowSeconds: int(LeaseWindow / time.Second), Ruleset: renderRuleset(kept)}
	if p.Closed == nil {
		p.Closed = []Port{}
	}
	if p.Refused == nil {
		p.Refused = []Refusal{}
	}
	for _, port := range kept {
		if !slices.Contains(st.Closed, port) {
			p.Close = append(p.Close, port)
		}
		if !slices.Contains(baseline, port) {
			p.Lease = true
		}
	}
	for _, port := range st.Closed {
		if !slices.Contains(kept, port) {
			p.Open = append(p.Open, port)
		}
	}
	return p
}

// screen sorts desired, drops what repeats, and sets aside the ports that cannot be closed.
func (m *Manager) screen(desired []Port) (kept []Port, refused []Refusal) {
	var protected []Port
	if m.protected != nil {
		protected = m.protected()
	}
	for _, p := range sortedUnique(desired) {
		switch {
		case !p.valid():
			refused = append(refused, Refusal{Port: p, Reason: "not a port"})
		case slices.Contains(protected, p):
			refused = append(refused, Refusal{Port: p, Reason: "this port keeps the admin able to reach the host"})
		default:
			kept = append(kept, p)
		}
	}
	return kept, refused
}

// confirmed is the set of closed ports that the admin has confirmed.
func confirmed(st State) []Port {
	if st.Pending != nil {
		return st.Pending.Previous
	}
	return st.Closed
}

// Apply closes exactly the ports of desired. A change that closes a port that was not closed before waits to be confirmed within
// LeaseWindow, and is rolled back if it is not; a change that only opens ports takes effect at once.
func (m *Manager) Apply(ctx context.Context, desired []Port) (Plan, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		return Plan{}, err
	}
	p := m.planLocked(st, desired)
	if len(p.Refused) > 0 {
		names := make([]string, len(p.Refused))
		for i, r := range p.Refused {
			names[i] = r.Port.String()
		}
		return p, fmt.Errorf("%w: %s", ErrRefused, strings.Join(names, ", "))
	}
	if err := m.applyLocked(ctx, p.Closed); err != nil {
		return p, err
	}
	next := State{Closed: p.Closed}
	if p.Lease {
		next.Pending = &Pending{Deadline: m.now().Add(LeaseWindow), Previous: confirmed(st)}
	}
	if err := m.store.Save(next); err != nil {
		// Rules that nothing remembers could not be rolled back.
		if rerr := m.applyLocked(ctx, st.Closed); rerr != nil {
			logger.Warning("portguard: cannot put the rules back after the state was not saved:", rerr)
		}
		return p, err
	}
	return p, nil
}

func (m *Manager) applyLocked(ctx context.Context, ports []Port) error {
	if len(ports) == 0 {
		return m.fw.Remove(ctx)
	}
	return m.fw.Apply(ctx, ports)
}

// Confirm keeps the change that waits to be confirmed.
func (m *Manager) Confirm() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		return err
	}
	if st.Pending == nil {
		return ErrNothingPending
	}
	st.Pending = nil
	return m.store.Save(st)
}

// Rollback puts back what was closed before the change that waits to be confirmed.
func (m *Manager) Rollback(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		return err
	}
	if st.Pending == nil {
		return ErrNothingPending
	}
	return m.rollbackLocked(ctx, st)
}

// rollbackLocked forgets the pending change before it undoes it, so that a rollback that fails is not tried again for ever; and
// when the earlier rules cannot be put back it removes the rules altogether, since an admin who cannot get in is the worse case.
func (m *Manager) rollbackLocked(ctx context.Context, st State) error {
	previous := st.Pending.Previous
	if err := m.store.Save(State{Closed: previous}); err != nil {
		return err
	}
	err := m.applyLocked(ctx, previous)
	if err == nil {
		return nil
	}
	logger.Warning("portguard: cannot put the earlier rules back, removing them:", err)
	if rerr := m.fw.Remove(ctx); rerr != nil {
		return errors.Join(err, rerr)
	}
	if serr := m.store.Save(State{}); serr != nil {
		return errors.Join(err, serr)
	}
	return err
}

// Tick rolls back a change whose time to be confirmed is up; the job that calls it runs every few seconds.
func (m *Manager) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil || st.Pending == nil || m.now().Before(st.Pending.Deadline) {
		return
	}
	logger.Warning("portguard: the change of the closed ports was not confirmed in time, rolling it back")
	if err := m.rollbackLocked(ctx, st); err != nil {
		logger.Warning("portguard: rollback failed:", err)
	}
}

// Restore puts the rules back when the panel starts, since the kernel forgets them at a reboot; a change that has waited for
// confirmation for too long is rolled back instead.
func (m *Manager) Restore(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st, err := m.store.Load()
	if err != nil {
		logger.Warning("portguard: cannot read the state:", err)
		return
	}
	if st.Pending != nil && !m.now().Before(st.Pending.Deadline) {
		logger.Warning("portguard: the change of the closed ports was not confirmed before the panel stopped, rolling it back")
		if err := m.rollbackLocked(ctx, st); err != nil {
			logger.Warning("portguard: rollback failed:", err)
		}
		return
	}
	if len(st.Closed) == 0 {
		return
	}
	if err := m.fw.Apply(ctx, st.Closed); err != nil {
		logger.Warning("portguard: cannot restore the closed ports:", err)
	}
}

func sortedUnique(ports []Port) []Port {
	out := slices.Clone(ports)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Proto != out[j].Proto {
			return out[i].Proto < out[j].Proto
		}
		return out[i].Number < out[j].Number
	})
	return slices.Compact(out)
}
