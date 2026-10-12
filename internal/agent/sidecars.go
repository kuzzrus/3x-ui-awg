package agent

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// How often the helper programs are looked at, which is how soon one that died is noticed.
	sidecarCheckEvery = 10 * time.Second
	// How long a program that died, or would not start, is left alone before it is started again,
	// and the longer wait after an install that failed, which costs a package-manager run each time.
	sidecarRestartAfter      = 30 * time.Second
	sidecarInstallRetryAfter = 5 * time.Minute
	// How much of what a program or its installer said is kept, from the end, where the reason is.
	maxSidecarMessage = 300
)

// sidecar is a helper program the agent runs beside its core.
type sidecar interface {
	Installed() bool
	Install(ctx context.Context) error
	Start() error
	Stop() error
	Running() bool
	// Result is the last thing the program said, or how it ended.
	Result() string
}

// defaultSidecars are the programs this build can run, by the names of the protocol.
func defaultSidecars() map[string]sidecar {
	return map[string]sidecar{agentproto.SidecarTor: torProgram{}, agentproto.SidecarPsiphon: &psiphonProgram{}}
}

// sidecarSlot is what the host knows of one program between two passes.
type sidecarSlot struct {
	state   string
	message string
	retryAt time.Time // a failed program is left alone until then
}

// sidecarHost runs the helper programs the master asks for, installs the ones the machine lacks, starts
// the ones that stopped, and remembers the list across a restart, since the master may be out of reach then.
type sidecarHost struct {
	state    *State
	programs map[string]sidecar
	now      func() time.Time         // time.Now unless a test sets it
	wake     map[string]chan struct{} // one for the supervisor of each program, which holds at most one poke

	mu      sync.Mutex
	enabled []string
	slots   map[string]*sidecarSlot
}

func newSidecarHost(state *State, programs map[string]sidecar) *sidecarHost {
	h := &sidecarHost{
		state:    state,
		programs: programs,
		now:      time.Now,
		wake:     make(map[string]chan struct{}, len(programs)),
		slots:    map[string]*sidecarSlot{},
	}
	for name := range programs {
		h.wake[name] = make(chan struct{}, 1)
	}
	enabled, err := state.Sidecars()
	if err != nil {
		logger.Warning("agent: the helper programs to run are unreadable, running none until the master says:", err)
	}
	h.enabled = enabled
	return h
}

// Set makes names the list of programs to run and answers at once: an install takes minutes, so the
// states say how far each has got. A list that is already the stored one changes nothing.
func (h *sidecarHost) Set(names []string) (agentproto.Sidecars, error) {
	enabled, err := agentproto.NormalizeSidecars(names)
	if err != nil {
		return agentproto.Sidecars{}, err
	}
	h.mu.Lock()
	changed := !slices.Equal(h.enabled, enabled)
	if changed {
		if err := h.state.SaveSidecars(enabled); err != nil {
			h.mu.Unlock()
			return agentproto.Sidecars{}, err
		}
		h.enabled = enabled
	}
	h.mu.Unlock()
	if changed {
		for _, wake := range h.wake {
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	return h.View(), nil
}

func (h *sidecarHost) View() agentproto.Sidecars {
	h.mu.Lock()
	enabled := append([]string{}, h.enabled...)
	states := h.statesLocked()
	h.mu.Unlock()
	h.sayLatest(states)
	return agentproto.Sidecars{Revision: agentproto.SidecarsRevision(enabled), Enabled: enabled, States: states}
}

// Revision is what the master compares with the list it wants the agent to run.
func (h *sidecarHost) Revision() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agentproto.SidecarsRevision(h.enabled)
}

// States is how each program the agent was told to run stands, never nil.
func (h *sidecarHost) States() []agentproto.SidecarState {
	h.mu.Lock()
	states := h.statesLocked()
	h.mu.Unlock()
	h.sayLatest(states)
	return states
}

func (h *sidecarHost) statesLocked() []agentproto.SidecarState {
	states := make([]agentproto.SidecarState, 0, len(h.enabled))
	for _, name := range h.enabled {
		slot := h.slots[name]
		if slot == nil {
			states = append(states, agentproto.SidecarState{Name: name, State: agentproto.SidecarPending})
			continue
		}
		states = append(states, agentproto.SidecarState{Name: name, State: slot.state, Message: slot.message})
	}
	return states
}

// sayLatest gives a running program's state what it said last, which changes without a pass, as Tor's
// does while it builds a circuit. It asks the program outside the lock, which a stop of one can hold.
func (h *sidecarHost) sayLatest(states []agentproto.SidecarState) {
	for i := range states {
		if program := h.programs[states[i].Name]; program != nil && states[i].State == agentproto.SidecarRunning {
			states[i].Message = tailOf(program.Result())
		}
	}
}

// Run keeps the programs as the list says until ctx ends, and stops them then. Each program has a
// supervisor of its own, so an install that takes minutes holds up that program and no other.
func (h *sidecarHost) Run(ctx context.Context) {
	defer h.stopAll()
	var supervisors sync.WaitGroup
	for _, name := range agentproto.KnownSidecars {
		if program := h.programs[name]; program != nil {
			supervisors.Go(func() { h.supervise(ctx, name, program) })
		}
	}
	supervisors.Wait()
}

func (h *sidecarHost) supervise(ctx context.Context, name string, program sidecar) {
	ticker := time.NewTicker(sidecarCheckEvery)
	defer ticker.Stop()
	for {
		h.tend(ctx, name, program)
		select {
		case <-ctx.Done():
			return
		case <-h.wake[name]:
		case <-ticker.C:
		}
	}
}

// tend brings one program to what the list says of it.
func (h *sidecarHost) tend(ctx context.Context, name string, program sidecar) {
	if ctx.Err() != nil {
		return
	}
	if h.wanted(name) {
		h.keepRunning(ctx, name, program)
	} else {
		h.keepStopped(name, program)
	}
}

func (h *sidecarHost) wanted(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Contains(h.enabled, name)
}

// slotOf copies the slot of a program, making a pending one the first time.
func (h *sidecarHost) slotOf(name string) sidecarSlot {
	h.mu.Lock()
	defer h.mu.Unlock()
	slot := h.slots[name]
	if slot == nil {
		slot = &sidecarSlot{state: agentproto.SidecarPending}
		h.slots[name] = slot
	}
	return *slot
}

func (h *sidecarHost) setSlot(name, state, message string, retryAt time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.slots[name] = &sidecarSlot{state: state, message: tailOf(message), retryAt: retryAt}
}

func (h *sidecarHost) keepStopped(name string, program sidecar) {
	if program.Running() {
		if err := program.Stop(); err != nil {
			logger.Warningf("agent: stopping %s: %v", name, err)
		}
	}
	h.mu.Lock()
	delete(h.slots, name)
	h.mu.Unlock()
}

func (h *sidecarHost) keepRunning(ctx context.Context, name string, program sidecar) {
	slot := h.slotOf(name)
	if program.Running() {
		if slot.state != agentproto.SidecarRunning {
			h.setSlot(name, agentproto.SidecarRunning, "", time.Time{})
		}
		return
	}
	if slot.state == agentproto.SidecarRunning {
		message := program.Result()
		if message == "" {
			message = "it exited"
		}
		logger.Warningf("agent: %s stopped: %s", name, tailOf(message))
		h.setSlot(name, agentproto.SidecarFailed, message, h.now().Add(sidecarRestartAfter))
		return
	}
	if slot.state == agentproto.SidecarFailed && h.now().Before(slot.retryAt) {
		return
	}
	if !program.Installed() {
		logger.Infof("agent: installing %s", name)
		h.setSlot(name, agentproto.SidecarInstalling, "", time.Time{})
		if err := program.Install(ctx); err != nil {
			logger.Warningf("agent: installing %s failed: %v", name, err)
			h.setSlot(name, agentproto.SidecarFailed, err.Error(), h.now().Add(sidecarInstallRetryAfter))
			return
		}
		// The list may have changed during the install, and a later pass starts or stops it as it says now.
		if ctx.Err() != nil || !h.wanted(name) {
			return
		}
	}
	if err := program.Start(); err != nil {
		logger.Warningf("agent: starting %s failed: %v", name, err)
		h.setSlot(name, agentproto.SidecarFailed, err.Error(), h.now().Add(sidecarRestartAfter))
		return
	}
	logger.Infof("agent: %s is running", name)
	h.setSlot(name, agentproto.SidecarRunning, "", time.Time{})
}

func (h *sidecarHost) stopAll() {
	for _, name := range agentproto.KnownSidecars {
		program := h.programs[name]
		if program == nil || !program.Running() {
			continue
		}
		if err := program.Stop(); err != nil {
			logger.Warningf("agent: stopping %s: %v", name, err)
		}
	}
}

// tailOf is the end of what a program or its installer said, on one line, at most maxSidecarMessage long.
func tailOf(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= maxSidecarMessage {
		return text
	}
	return "…" + string(runes[len(runes)-maxSidecarMessage:])
}
