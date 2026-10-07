package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// Variables so tests can shorten them.
var (
	startTimeout   = 10 * time.Second
	readySettle    = 300 * time.Millisecond
	testTimeout    = 20 * time.Second
	superviseEvery = 2 * time.Second
	maxBackoff     = time.Minute
)

// ErrNoConfig is what Restart answers before the master has pushed a config.
var ErrNoConfig = errors.New("no config has been applied yet")

// ErrCoreRestarted means the core was replaced while its counters were being read.
var ErrCoreRestarted = errors.New("the core restarted while its counters were read")

// applied is a config the core runs, or last ran.
type applied struct {
	revision string
	restart  bool
	body     []byte
	cfg      *xray.Config
}

// Core owns the Xray process: it applies pushed configs, goes back to the previous one
// when a new one does not start, and brings the core back after a crash.
type Core struct {
	state  *State
	logDir string

	applyMu sync.Mutex // serializes everything that changes the process

	mu        sync.Mutex // guards the fields below and is never held across slow work
	process   *xray.Process
	current   *applied
	savedRev  string
	startedAt int64
	version   string
	lastError string
	closed    bool
	retryAt   time.Time
	backoff   time.Duration
}

func NewCore(state *State, logDir string) *Core {
	return &Core{state: state, logDir: logDir}
}

// Snapshot is what the status endpoint reports about the core.
type Snapshot struct {
	Revision    string
	XrayVersion string
	XrayState   string
	XrayError   string
}

func (c *Core) Snapshot() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Snapshot{XrayVersion: c.version, XrayError: c.lastError}
	if c.current != nil {
		s.Revision = c.current.revision
	}
	switch {
	case c.runningLocked():
		s.XrayState = agentproto.XrayStateRunning
	case c.lastError != "":
		s.XrayState = agentproto.XrayStateError
	default:
		s.XrayState = agentproto.XrayStateStopped
	}
	return s
}

func (c *Core) runningLocked() bool {
	return c.process != nil && c.process.IsRunning()
}

// Boot starts the last good config without waiting for the master. A failure is
// returned for the log, and the supervisor keeps trying.
func (c *Core) Boot(ctx context.Context) error {
	saved, err := c.state.LastGood()
	if err != nil || saved == nil {
		return err
	}
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	next, err := c.build(saved.Body, saved.RestartOnUserRemoval)
	if err != nil {
		c.setError(err.Error())
		return err
	}
	c.mu.Lock()
	c.current, c.savedRev = next, next.revision
	c.mu.Unlock()
	if err := c.start(ctx, next); err != nil {
		c.setError(err.Error())
		return err
	}
	return nil
}

// Apply makes the core run body, the config the master rendered. A config the core
// cannot run leaves the previous one in place and comes back as a *ConfigError.
func (c *Core) Apply(ctx context.Context, body []byte, restartOnUserRemoval bool) (agentproto.ConfigResponse, error) {
	ctx = context.WithoutCancel(ctx)
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	revision := agentproto.RevisionOf(body, restartOnUserRemoval)
	mode, err := c.apply(ctx, body, restartOnUserRemoval, revision)
	if err != nil {
		c.setError(fmt.Sprintf("config %.8s not applied: %v", revision, err))
		return agentproto.ConfigResponse{}, err
	}
	c.setError("")
	return agentproto.ConfigResponse{Revision: revision, Applied: mode, XrayState: c.Snapshot().XrayState}, nil
}

func (c *Core) apply(ctx context.Context, body []byte, restartOnUserRemoval bool, revision string) (string, error) {
	c.mu.Lock()
	cur, running := c.current, c.runningLocked()
	c.mu.Unlock()
	if running && cur.revision == revision {
		return agentproto.AppliedNoop, c.persist(cur)
	}

	next, err := c.build(body, restartOnUserRemoval)
	if err != nil {
		return "", err
	}
	if err := c.testConfig(ctx, next.cfg); err != nil {
		return "", err
	}
	mode, err := c.switchTo(ctx, next)
	if err != nil {
		return "", err
	}
	return mode, c.persist(next)
}

func (c *Core) build(body []byte, restartOnUserRemoval bool) (*applied, error) {
	cfg, err := prepareConfig(body, c.logDir)
	if err != nil {
		return nil, err
	}
	return &applied{
		revision: agentproto.RevisionOf(body, restartOnUserRemoval),
		restart:  restartOnUserRemoval,
		body:     body,
		cfg:      cfg,
	}, nil
}

// testConfig has the core check the config itself, so a mistake is caught while the
// previous config still serves.
func (c *Core) testConfig(ctx context.Context, cfg *xray.Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.state.dir, "candidate-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(raw)
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}

	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, xray.GetBinaryPath(), "-test", "-c", file.Name()).CombinedOutput()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		return fmt.Errorf("xray -test did not finish in %s", testTimeout)
	case errors.As(err, &exit):
		return configErrorf("the core rejected the config: %s", tail(out))
	default:
		return fmt.Errorf("run xray -test: %w", err)
	}
}

// tail keeps the end of the core's output, where it says what was wrong.
func tail(out []byte) string {
	const limit = 1000
	text := strings.TrimSpace(string(out))
	if len(text) > limit {
		text = text[len(text)-limit:]
		if i := strings.IndexByte(text, '\n'); i >= 0 {
			text = text[i+1:]
		}
	}
	return text
}

// switchTo takes the running core to next the cheapest way: not at all when only
// formatting changed, through its API when it can, by a restart otherwise.
func (c *Core) switchTo(ctx context.Context, next *applied) (string, error) {
	c.mu.Lock()
	p, prev := c.process, c.current
	c.mu.Unlock()

	if p != nil && p.IsRunning() {
		if diff, ok := xray.ComputeHotDiff(p.GetConfig(), next.cfg); ok && diff.Empty() {
			p.SetConfig(next.cfg)
			c.setCurrent(next)
			return agentproto.AppliedNoop, nil
		}
		dropsUsers := func(diff *xray.HotDiff) bool { return next.restart && diff.DropsUsers() }
		if xray.ApplyHot(p, next.cfg, dropsUsers) {
			c.setCurrent(next)
			return agentproto.AppliedHot, nil
		}
	}
	return agentproto.AppliedRestart, c.replace(ctx, prev, next)
}

// replace restarts the core into next, and into prev again when next does not start.
func (c *Core) replace(ctx context.Context, prev, next *applied) error {
	c.stopProcess()
	err := c.start(ctx, next)
	if err == nil {
		return nil
	}
	logger.Warning("agent: the new config does not start, going back to the previous one:", err)
	if prev != nil && prev.revision != next.revision {
		if rollbackErr := c.start(ctx, prev); rollbackErr != nil {
			logger.Error("agent: the previous config does not start either:", rollbackErr)
		}
	}
	return err
}

func (c *Core) stopProcess() {
	c.mu.Lock()
	p := c.process
	c.process, c.startedAt = nil, 0
	c.mu.Unlock()
	if p != nil && p.IsRunning() {
		if err := p.Stop(); err != nil {
			logger.Warning("agent: stopping xray failed:", err)
		}
	}
}

// start launches a new core process with a and waits until it is serving. Only a
// core that got that far replaces what the Core reports as running.
func (c *Core) start(ctx context.Context, a *applied) error {
	p := xray.NewProcess(a.cfg)
	if err := p.Start(); err != nil {
		return fmt.Errorf("start xray: %w", err)
	}
	if err := waitReady(ctx, p); err != nil {
		if p.IsRunning() {
			_ = p.Stop()
		}
		return err
	}
	c.mu.Lock()
	c.process, c.current, c.startedAt, c.version = p, a, time.Now().UnixMilli(), p.GetXrayVersion()
	c.backoff, c.retryAt = 0, time.Time{}
	c.mu.Unlock()
	return nil
}

// waitReady waits for the core's API port to open and the process to stay up a moment
// after it, since a core that cannot bind one of its inbounds exits just after that.
func waitReady(ctx context.Context, p *xray.Process) error {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(p.GetAPIPort()))
	dialer := net.Dialer{Timeout: 200 * time.Millisecond}
	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if !p.IsRunning() {
			return configErrorf("xray exited right after it started: %s", p.GetResult())
		}
		if conn, err := dialer.DialContext(ctx, "tcp", addr); err == nil {
			_ = conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return configErrorf("xray did not open its api port within %s: %s", startTimeout, p.GetResult())
		case <-tick.C:
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(readySettle):
	}
	if !p.IsRunning() {
		return configErrorf("xray exited right after it started: %s", p.GetResult())
	}
	return nil
}

func (c *Core) persist(a *applied) error {
	c.mu.Lock()
	saved := c.savedRev == a.revision
	c.mu.Unlock()
	if saved {
		return nil
	}
	if err := c.state.SaveLastGood(a.body, a.restart); err != nil {
		return fmt.Errorf("save the last good config: %w", err)
	}
	c.mu.Lock()
	c.savedRev = a.revision
	c.mu.Unlock()
	return nil
}

func (c *Core) setCurrent(a *applied) {
	c.mu.Lock()
	c.current = a
	c.mu.Unlock()
}

func (c *Core) setError(msg string) {
	c.mu.Lock()
	c.lastError = msg
	c.mu.Unlock()
}

// Restart restarts the core with the config it last ran.
func (c *Core) Restart(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	cur := c.current
	c.mu.Unlock()
	if cur == nil {
		return ErrNoConfig
	}
	c.stopProcess()
	if err := c.start(ctx, cur); err != nil {
		c.setError(err.Error())
		return err
	}
	c.setError("")
	return nil
}

// Run keeps the core alive until ctx ends: a core that died is started again with the
// config it last ran, backing off while that keeps failing.
func (c *Core) Run(ctx context.Context) {
	tick := time.NewTicker(superviseEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			c.supervise(ctx)
		}
	}
}

func (c *Core) supervise(ctx context.Context) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()

	c.mu.Lock()
	cur, p, closed, retryAt := c.current, c.process, c.closed, c.retryAt
	c.mu.Unlock()
	if closed || cur == nil || (p != nil && p.IsRunning()) || time.Now().Before(retryAt) {
		return
	}
	logger.Warning("agent: xray is not running, starting it with the last good config")
	if err := c.start(ctx, cur); err != nil {
		c.mu.Lock()
		c.backoff = min(max(2*c.backoff, time.Second), maxBackoff)
		c.retryAt = time.Now().Add(c.backoff)
		c.lastError = err.Error()
		c.mu.Unlock()
		return
	}
	c.setError("")
}

// Close stops the core for good, so the supervisor does not bring it back.
func (c *Core) Close() {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.mu.Lock()
	c.closed = true
	c.mu.Unlock()
	c.stopProcess()
}

// Stats reads the core's cumulative counters. They restart from zero with every
// core process, which StartedAt tells apart.
func (c *Core) Stats() (agentproto.Stats, error) {
	stats := agentproto.Stats{
		Inbounds: map[string]agentproto.Counter{},
		Users:    map[string]agentproto.Counter{},
		Online:   []string{},
	}
	c.mu.Lock()
	p, startedAt := c.process, c.startedAt
	c.mu.Unlock()
	if p == nil || !p.IsRunning() {
		return stats, nil
	}

	api := &xray.XrayAPI{}
	if err := api.Init(p.GetAPIPort()); err != nil {
		return stats, err
	}
	defer api.Close()
	inbounds, users, err := api.GetCounters()
	if err != nil {
		return stats, err
	}
	// The online list is a courtesy: the counters are what the master accounts from.
	online, err := api.GetOnlineUsers()
	if err != nil {
		logger.Debug("agent: online users unavailable:", err)
	}

	c.mu.Lock()
	replaced := c.process != p || c.startedAt != startedAt
	c.mu.Unlock()
	if replaced {
		return stats, ErrCoreRestarted
	}
	stats.XrayStartedAt = startedAt
	for tag, n := range inbounds {
		stats.Inbounds[tag] = agentproto.Counter{Up: n.Up, Down: n.Down}
	}
	for email, n := range users {
		stats.Users[email] = agentproto.Counter{Up: n.Up, Down: n.Down}
	}
	for _, user := range online {
		stats.Online = append(stats.Online, user.Email)
	}
	slices.Sort(stats.Online)
	return stats, nil
}
