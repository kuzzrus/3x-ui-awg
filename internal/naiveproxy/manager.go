package naiveproxy

import (
	"fmt"
	"os"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

func configDir() string             { return Dir() }
func configPathForID(id int) string { return fmt.Sprintf("%s/Caddyfile-%d", configDir(), id) }

// writeDecoyContent renders and writes the page file_server serves. Never
// errors, matching frontproxy's own decoy philosophy: log, don't block Start.
func writeDecoyContent(inst Instance) {
	dir := decoyDirForID(inst.Id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warningf("naiveproxy: inbound %d: cannot create decoy dir %s: %v", inst.Id, dir, err)
		return
	}
	body, err := frontproxy.RenderDecoyTemplate(frontproxy.DefaultDecoyTemplate, inst.Domain)
	if err != nil {
		logger.Warningf("naiveproxy: inbound %d: cannot render decoy content: %v", inst.Id, err)
		return
	}
	if err := os.WriteFile(dir+"/index.html", body, 0o600); err != nil {
		logger.Warningf("naiveproxy: inbound %d: cannot write decoy content to %s: %v", inst.Id, dir, err)
	}
}

// managed pairs a running process with the exact Caddyfile text it was
// started from, so ensureLocked can tell "nothing changed" from "restart needed".
type managed struct {
	proc        *Process
	fingerprint string
}

// Manager owns every Naive-backed inbound's Caddy process, one per inbound,
// mirroring internal/mtproto's Manager shape.
type Manager struct {
	mu    sync.Mutex
	procs map[int]*managed
	swept bool
	// meters outlive their process, so bytes a stopped Caddy's last tunnels
	// reported are still handed over by the next CollectTraffic.
	meters map[int]*meter
	// failing holds each inbound's last logged Reconcile failure, so one that lasts
	// across ticks (a missing engine, most often) is reported once, not every tick.
	failing map[int]string
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the process-wide NaiveProxy manager singleton.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = &Manager{procs: map[int]*managed{}, meters: map[int]*meter{}} })
	return manager
}

// meterLocked returns inbound id's meter, created on first use and retagged so
// it follows the inbound's current tag.
func (m *Manager) meterLocked(id int, tag string) *meter {
	if m.meters == nil {
		m.meters = map[int]*meter{}
	}
	mt, ok := m.meters[id]
	if !ok {
		mt = newMeter(tag)
		m.meters[id] = mt
	}
	mt.setTag(tag)
	return mt
}

// sweepOrphansLocked kills stray caddy processes from a previous run, once
// per process lifetime -- before m.procs is trusted as the full picture.
func (m *Manager) sweepOrphansLocked() {
	if m.swept {
		return
	}
	m.swept = true
	if n := killStrayCaddyProcesses(BinPath()); n > 0 {
		logger.Warningf("naiveproxy: terminated %d orphaned caddy process(es) from a previous run", n)
	}
}

// Ensure brings the running process for inst.Id in line with inst.
func (m *Manager) Ensure(inst Instance) error {
	m.mu.Lock()
	m.sweepOrphansLocked()
	proc, err := m.ensureLocked(inst)
	m.mu.Unlock()
	if err != nil || proc == nil {
		return err
	}
	return m.awaitReady(inst.Id, proc)
}

// ensureLocked is Ensure's fast, non-blocking part; returns the freshly
// spawned process to await outside the lock, nil if nothing new started.
func (m *Manager) ensureLocked(inst Instance) (*Process, error) {
	if len(inst.Clients) == 0 {
		m.removeLocked(inst.Id)
		return nil, nil
	}

	fp, err := renderCaddyfile(inst)
	if err != nil {
		return nil, err
	}
	// Before any write: with no engine nothing can start, and each Reconcile tick would create
	// and delete the same files. Tracked instances skip this, a running one needs no engine.
	if _, tracked := m.procs[inst.Id]; !tracked && !IsInstalled() {
		return nil, ErrNotInstalled
	}
	mt := m.meterLocked(inst.Id, inst.Tag)
	// Unconditional: the decoy dir is keyed by Id not Domain, so a
	// domain-only change would never show up in fp if this were gated on it.
	writeDecoyContent(inst)

	if cur, ok := m.procs[inst.Id]; ok {
		if cur.proc.IsRunning() && cur.fingerprint == fp {
			return nil, nil
		}
		_ = cur.proc.Stop()
		delete(m.procs, inst.Id)
	}

	if err := os.MkdirAll(configDir(), 0o700); err != nil {
		return nil, fmt.Errorf("naiveproxy: cannot create %s: %w", configDir(), err)
	}
	cfgPath := configPathForID(inst.Id)
	if err := os.WriteFile(cfgPath, []byte(fp), 0o600); err != nil {
		return nil, fmt.Errorf("naiveproxy: cannot write %s: %w", cfgPath, err)
	}

	// Only now: the process stopped above wrote its last lines under the old routing.
	mt.setRouted(inst.RouteThroughXray)
	proc := newProcess(cfgPath, inst.ListenAddr, fmt.Sprintf("inbound %d", inst.Id), mt)
	if err := proc.Start(); err != nil {
		// Never tracked, so removeLocked would not clean these up later.
		_ = os.Remove(cfgPath)
		_ = os.RemoveAll(decoyDirForID(inst.Id))
		return nil, err
	}
	m.procs[inst.Id] = &managed{proc: proc, fingerprint: fp}
	return proc, nil
}

// awaitReady blocks (outside m.mu) until proc is actually serving, tearing
// it back down on failure so a half-started instance is never left tracked.
func (m *Manager) awaitReady(id int, proc *Process) error {
	if err := proc.WaitReady(); err != nil {
		m.Remove(id)
		return fmt.Errorf("naiveproxy: inbound %d: %w", id, err)
	}
	logger.Infof("naiveproxy: started caddy for inbound %d", id)
	return nil
}

// Remove stops and forgets the Caddy process for an inbound id.
func (m *Manager) Remove(id int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.removeLocked(id)
}

func (m *Manager) removeLocked(id int) {
	cur, ok := m.procs[id]
	if !ok {
		return
	}
	_ = cur.proc.Stop()
	delete(m.procs, id)
	_ = os.Remove(configPathForID(id))
	_ = os.RemoveAll(decoyDirForID(id))
	logger.Infof("naiveproxy: stopped caddy for inbound %d", id)
}

// isNewFailureLocked reports whether err is news for inbound id -- its first failure
// or a different one from the last it logged -- and remembers it.
func (m *Manager) isNewFailureLocked(id int, err error) bool {
	msg := err.Error()
	if last, ok := m.failing[id]; ok && last == msg {
		return false
	}
	if m.failing == nil {
		m.failing = map[int]string{}
	}
	m.failing[id] = msg
	return true
}

// Reconcile drives the running set toward desired -- spawns happen under
// m.mu, readiness waits after releasing it, so one slow instance can't stall the rest.
func (m *Manager) Reconcile(desired []Instance) (changed bool) {
	m.mu.Lock()
	m.sweepOrphansLocked()

	want := make(map[int]struct{}, len(desired))
	for _, inst := range desired {
		want[inst.Id] = struct{}{}
	}
	for id := range m.procs {
		if _, ok := want[id]; !ok {
			m.removeLocked(id)
			changed = true
		}
	}
	for id := range m.failing {
		if _, ok := want[id]; !ok {
			delete(m.failing, id)
		}
	}

	type pending struct {
		id   int
		proc *Process
	}
	var toAwait []pending
	for _, inst := range desired {
		proc, err := m.ensureLocked(inst)
		if err != nil {
			if m.isNewFailureLocked(inst.Id, err) {
				logger.Warningf("naiveproxy: reconcile failed for inbound %d: %v", inst.Id, err)
			}
			continue
		}
		delete(m.failing, inst.Id)
		if proc != nil {
			toAwait = append(toAwait, pending{inst.Id, proc})
		}
	}
	m.mu.Unlock()

	// changed becomes true only on an actual success -- a spawn that never
	// gets ready must not report a change on every retry tick.
	for _, p := range toAwait {
		if err := m.awaitReady(p.id, p.proc); err != nil {
			logger.Warningf("naiveproxy: %v", err)
			continue
		}
		changed = true
	}
	return changed
}

// StopAll stops every managed Caddy process. Called on panel shutdown.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.procs {
		m.removeLocked(id)
	}
}

// IsRunning reports whether inbound id's Caddy process is currently running.
func (m *Manager) IsRunning(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.procs[id]
	return ok && cur.proc.IsRunning()
}

// CollectTraffic returns each client's byte delta since the last call, plus the emails
// that moved bytes. A tunnel counts only once it closes, so a long one lands late, at once.
func (m *Manager) CollectTraffic() ([]Traffic, []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Traffic
	var online []string
	for id, mt := range m.meters {
		for _, t := range mt.drain() {
			out = append(out, t)
			online = append(online, t.Email)
		}
		// Drained just above, and no process left to write more.
		if _, alive := m.procs[id]; !alive {
			delete(m.meters, id)
		}
	}
	return out, online
}
