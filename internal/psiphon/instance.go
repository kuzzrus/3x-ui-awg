package psiphon

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// How a tunnel that stops without being told to is brought back. The values are variables so tests can
// shorten them.
var (
	// restartBackoff is the wait after the first failed start; each further failure doubles it up to maxRestartBackoff.
	restartBackoff    = 30 * time.Second
	maxRestartBackoff = 5 * time.Minute
	// stableAfter is how long a process must have run for its end to count as a crash to restart at once,
	// not as one more failed start of a process that cannot stay up.
	stableAfter = time.Minute
)

// InstanceState is how one tunnel stands: the process is up, the tunnel it reports, the last thing it said, and why the last start failed.
type InstanceState struct {
	Running bool
	Tunnel  TunnelStatus
	LastLog string
	Error   string
}

// life is the supervision state of one instance: whether it should be running and, when it is not, when a
// new start may be tried.
type life struct {
	wanted    bool
	startedAt time.Time // when the running process came up; zero once its end was accounted for
	retryAt   time.Time
	delay     time.Duration
	lastErr   string
}

func (l *life) started(now time.Time) { l.startedAt, l.lastErr = now, "" }

// noteDown counts, once, the end of a process that should be running: one that lasted less than stableAfter is a
// failed start and waits longer, one that lasted longer is restarted at once and clears the earlier failures.
func (l *life) noteDown(now time.Time, reason string) {
	if l.startedAt.IsZero() {
		return
	}
	if reason != "" {
		l.lastErr = reason
	}
	if now.Sub(l.startedAt) < stableAfter {
		l.failed(now, nil)
		return
	}
	l.delay, l.retryAt, l.startedAt = 0, now, time.Time{}
}

func (l *life) failed(now time.Time, err error) {
	if err != nil {
		l.lastErr = err.Error()
	}
	l.delay = min(max(2*l.delay, restartBackoff), maxRestartBackoff)
	l.retryAt = now.Add(l.delay)
	l.startedAt = time.Time{}
}

// instance is one tunnel-core process the panel keeps, the default or a country. A start waits seconds for the
// listener, so the lifecycle has a lock of its own and the cheap state another: status never waits for a start.
type instance struct {
	tag string // "" for the default instance, "[DE]" for a country: how its lines read in the panel log
	// spec resolves the files and the port when they are needed, since the bin folder is read from the environment.
	spec func() (spec, error)
	// prepare writes what the process reads before it starts; nil for none.
	prepare func() error

	lifecycle sync.Mutex // one start, stop or restart at a time
	mu        sync.Mutex // guards proc and life, and is never held across a wait
	proc      *Process
	life      life
}

func newDefaultInstance() *instance {
	return &instance{spec: func() (spec, error) { return defaultSpec(), nil }}
}

func newCountryInstance(c Country) *instance {
	return &instance{
		tag:     "[" + c.Region + "]",
		spec:    func() (spec, error) { return countrySpec(c) },
		prepare: func() error { return renderCountryConfig(c) },
	}
}

func (in *instance) isRunning() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.proc != nil && in.proc.IsRunning()
}

// startLocked launches the process unless one is up, and waits until its SOCKS listener accepts connections.
// started is false when one was up already. The caller holds the lifecycle lock.
func (in *instance) startLocked() (started bool, err error) {
	if in.isRunning() {
		return false, nil
	}
	if !IsInstalled() {
		return false, fmt.Errorf("Psiphon is not installed")
	}
	if !IsConfigured() {
		return false, fmt.Errorf("Psiphon has no config -- upload a psiphon.config first")
	}
	sp, err := in.spec()
	if err != nil {
		return false, err
	}
	if !portFree(sp.port) {
		return false, fmt.Errorf("port %d is held by another process", sp.port)
	}
	if in.prepare != nil {
		if err := in.prepare(); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(sp.data, 0o700); err != nil {
		return false, fmt.Errorf("creating %s: %w", sp.data, err)
	}
	// Each run starts its notice log fresh -- -notices only ever appends, and the tunnel state
	// should reflect this process, not a stale prior run.
	_ = os.Remove(sp.notices)
	proc := newProcess(sp, in.tag)
	if err := proc.Start(); err != nil {
		return false, err
	}
	if err := waitForListener(net.JoinHostPort("127.0.0.1", strconv.Itoa(sp.port)), proc); err != nil {
		_ = proc.Stop()
		return false, err
	}
	in.mu.Lock()
	in.proc = proc
	in.mu.Unlock()
	logger.Infof("psiphon%s: started on 127.0.0.1:%d", in.tag, sp.port)
	return true, nil
}

// start brings the tunnel up and records that it should stay up, which is what makes the supervisor bring
// it back after a crash. A start that fails is the caller's to see and is not retried on its own.
func (in *instance) start(now time.Time) error {
	in.lifecycle.Lock()
	defer in.lifecycle.Unlock()
	started, err := in.startLocked()
	in.mu.Lock()
	defer in.mu.Unlock()
	if err != nil {
		in.life.lastErr = err.Error()
		return err
	}
	in.life.wanted = true
	if started {
		in.life.started(now)
	}
	return nil
}

// startSupervised is start for a restore at boot: it records that the tunnel should run before it tries, so a
// failure nobody was there to see is retried by the supervisor.
func (in *instance) startSupervised(now time.Time) error {
	in.lifecycle.Lock()
	defer in.lifecycle.Unlock()
	in.mu.Lock()
	in.life.wanted = true
	in.mu.Unlock()
	started, err := in.startLocked()
	in.mu.Lock()
	defer in.mu.Unlock()
	if err != nil {
		in.life.failed(now, err)
		return err
	}
	if started {
		in.life.started(now)
	}
	return nil
}

// stop takes the tunnel down and records that it is meant to stay down. A no-op when it is down already.
func (in *instance) stop() error {
	in.lifecycle.Lock()
	defer in.lifecycle.Unlock()
	return in.stopLocked()
}

func (in *instance) stopLocked() error {
	in.mu.Lock()
	in.life.wanted = false
	// A stop by the operator ends the story of an earlier failure: its reason and its back-off.
	in.life.lastErr, in.life.delay, in.life.retryAt = "", 0, time.Time{}
	proc := in.proc
	in.proc = nil
	in.mu.Unlock()
	if proc == nil || !proc.IsRunning() {
		return nil
	}
	return proc.Stop()
}

// restart stops the process and starts it again, keeping whether it should be running: Psiphon does not
// hot-reload its config.
func (in *instance) restart(now time.Time) error {
	in.lifecycle.Lock()
	defer in.lifecycle.Unlock()
	in.mu.Lock()
	wanted := in.life.wanted
	in.life.wanted = false // so the end of the old process is not taken for a crash
	proc := in.proc
	in.proc = nil
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		in.life.wanted = wanted
		in.mu.Unlock()
	}()
	if proc != nil && proc.IsRunning() {
		if err := proc.Stop(); err != nil {
			return err
		}
	}
	started, err := in.startLocked()
	in.mu.Lock()
	defer in.mu.Unlock()
	if err != nil {
		in.life.lastErr = err.Error()
		return err
	}
	if started {
		in.life.started(now)
	}
	return nil
}

// downLocked reports whether the tunnel should be running and is not. The caller holds in.mu.
func (in *instance) downLocked() bool {
	return in.life.wanted && (in.proc == nil || !in.proc.IsRunning())
}

func (in *instance) down() bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.downLocked()
}

// due accounts for the end of a process that should be running and says whether its back-off allows a new start.
// The lock is released by a defer, so a look that panics leaves none held.
func (in *instance) due(now time.Time) bool {
	in.mu.Lock()
	defer in.mu.Unlock()
	if !in.downLocked() {
		return false
	}
	reason := ""
	if in.proc != nil {
		reason = in.proc.GetResult()
	}
	in.life.noteDown(now, reason)
	return !now.Before(in.life.retryAt)
}

// check is one look of the supervisor: a tunnel that should be running and is not is started again, as soon as
// its back-off allows.
func (in *instance) check(now time.Time) {
	if !in.due(now) {
		return
	}

	in.lifecycle.Lock()
	defer in.lifecycle.Unlock()
	// An operator may have stopped or started it while this waited for the lock.
	if !in.down() {
		return
	}
	started, err := in.startLocked()
	in.mu.Lock()
	defer in.mu.Unlock()
	switch {
	case err != nil:
		in.life.failed(now, err)
		logger.Warningf("psiphon%s: restart failed, next try in %s: %v", in.tag, in.life.delay, err)
	case started:
		in.life.started(now)
		logger.Infof("psiphon%s: restarted after it stopped", in.tag)
	}
}

// state is how the tunnel stands, read without waiting for a start in progress.
func (in *instance) state() InstanceState {
	in.mu.Lock()
	proc, lastErr := in.proc, in.life.lastErr
	in.mu.Unlock()
	st := InstanceState{Error: lastErr}
	if proc == nil {
		return st
	}
	st.Running = proc.IsRunning()
	st.LastLog = proc.GetResult()
	if st.Running {
		if sp, err := in.spec(); err == nil {
			st.Tunnel, _ = currentTunnel(sp.notices)
		}
	}
	return st
}

// result is the last line the process wrote or the way it ended, and failing that why it could not start.
func (in *instance) result() string {
	st := in.state()
	if st.LastLog != "" {
		return st.LastLog
	}
	return st.Error
}
