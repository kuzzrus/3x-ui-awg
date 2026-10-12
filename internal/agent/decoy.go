package agent

import (
	"context"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/adguard"
	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// How often the decoy is looked at, and how long one that would not start is left alone.
	decoyCheckEvery   = 10 * time.Second
	decoyRestartAfter = 30 * time.Second

	// decoyListen is where the decoy listens: only the REALITY fallback of the core on this server may reach it.
	decoyListen = "127.0.0.1"
	// decoyCertsDir is the folder of the state folder that holds the certificates the decoy obtained.
	decoyCertsDir = "decoy-certs"
)

// decoyRunner is the TLS listener the decoy is served from, which internal/frontproxy provides.
type decoyRunner interface {
	Start(opts frontproxy.Options) error
	Reload(opts frontproxy.Options)
	Stop() error
	IsRunning() bool
	CertStatus() frontproxy.CertStatus
}

// frontProxyRunner is the one reverse proxy a process may run, here without a panel behind it.
type frontProxyRunner struct{}

func (frontProxyRunner) Start(opts frontproxy.Options) error {
	return frontproxy.GetManager().Start(opts)
}

func (frontProxyRunner) Reload(opts frontproxy.Options) { frontproxy.GetManager().Reload(opts) }

func (frontProxyRunner) Stop() error { return frontproxy.GetManager().Stop() }

func (frontProxyRunner) IsRunning() bool { return frontproxy.GetManager().IsRunning() }

func (frontProxyRunner) CertStatus() frontproxy.CertStatus { return frontproxy.CurrentCertStatus() }

// helperPrograms is what the decoy asks of the host of the helper programs: one to run for it, and how it stands.
type helperPrograms interface {
	Require(name string, on bool)
	StateOf(name string) (state, message string)
}

// decoyHost serves the decoy the master asks for, starts it again when it would not start, and remembers
// it across a restart, since the master may be out of reach then.
type decoyHost struct {
	state       *State
	runner      decoyRunner
	helpers     helperPrograms
	adguardPort func() (int, error) // adguard.WebPort unless a test sets it
	seed        string              // makes the page of this node unlike that of another
	storage     string              // where the certificates are kept
	now         func() time.Time
	wake        chan struct{}
	// privateProxy lets the proxied site be on a private address, which only a test with a site of its own on the loopback wants.
	privateProxy bool

	mu      sync.Mutex
	spec    agentproto.DecoySpec  // what is wanted
	applied *agentproto.DecoySpec // what the listener runs with, nil when it does not run
	failure string
	waiting string // what the start is held up by, which is AdGuard Home that is not there yet
	retryAt time.Time
}

func newDecoyHost(state *State, runner decoyRunner, helpers helperPrograms, seed, storage string) *decoyHost {
	h := &decoyHost{
		state: state, runner: runner, helpers: helpers, adguardPort: adguard.WebPort,
		seed: seed, storage: storage, now: time.Now, wake: make(chan struct{}, 1),
	}
	spec, err := state.Decoy()
	if err != nil {
		logger.Warning("agent: the decoy to serve is unreadable, serving none until the master says:", err)
	}
	h.spec = spec
	return h
}

func decoyRules() agentproto.DecoyRules {
	return agentproto.DecoyRules{Templates: frontproxy.DecoyTemplateNames()}
}

// Set makes spec the decoy and answers at once with how it stands, which the loop then brings about.
// A decoy that is already the stored one changes nothing.
func (h *decoyHost) Set(spec agentproto.DecoySpec) (agentproto.Decoy, error) {
	rules := decoyRules()
	rules.PrivateProxy = h.privateProxy
	spec, err := agentproto.NormalizeDecoy(spec, rules)
	if err != nil {
		return agentproto.Decoy{}, err
	}
	h.mu.Lock()
	changed := h.spec != spec
	if changed {
		if err := h.state.SaveDecoy(spec); err != nil {
			h.mu.Unlock()
			return agentproto.Decoy{}, err
		}
		h.spec = spec
		h.failure, h.waiting, h.retryAt = "", "", time.Time{}
	}
	h.mu.Unlock()
	if changed {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
	return h.View(), nil
}

func (h *decoyHost) View() agentproto.Decoy {
	h.mu.Lock()
	spec := h.spec
	h.mu.Unlock()
	return agentproto.Decoy{Revision: agentproto.DecoyRevision(spec), Spec: spec, State: h.State()}
}

// Revision is what the master compares with the decoy it wants the agent to serve.
func (h *decoyHost) Revision() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return agentproto.DecoyRevision(h.spec)
}

// State is how the decoy stands. The certificate is asked of the listener outside the lock.
func (h *decoyHost) State() agentproto.DecoyState {
	h.mu.Lock()
	spec, failure, waiting, applied := h.spec, h.failure, h.waiting, h.applied != nil
	h.mu.Unlock()
	switch {
	case spec.Mode == agentproto.DecoyNone:
		return agentproto.DecoyState{State: agentproto.DecoyStateOff}
	case failure != "":
		return agentproto.DecoyState{State: agentproto.DecoyStateFailed, Message: failure}
	case waiting != "" || !applied:
		return agentproto.DecoyState{State: agentproto.DecoyStateStarting, Message: waiting}
	}
	state := agentproto.DecoyState{State: agentproto.DecoyStateRunning}
	cert := h.runner.CertStatus()
	switch cert.State {
	case frontproxy.CertStateObtaining:
		state.Cert = agentproto.DecoyCertObtaining
	case frontproxy.CertStateObtained:
		state.Cert = agentproto.DecoyCertObtained
		if cert.NotAfter != nil {
			state.CertNotAfter = cert.NotAfter.Unix()
		}
	case frontproxy.CertStateFailed:
		state.Cert, state.Message = agentproto.DecoyCertFailed, tailOf(cert.Error)
	}
	return state
}

// Run keeps the listener as the decoy says until ctx ends, and stops it then.
func (h *decoyHost) Run(ctx context.Context) {
	defer h.stop()
	ticker := time.NewTicker(decoyCheckEvery)
	defer ticker.Stop()
	for {
		h.reconcile()
		select {
		case <-ctx.Done():
			return
		case <-h.wake:
		case <-ticker.C:
		}
	}
}

// listenerOf is what a running listener cannot take another of without a restart.
func listenerOf(spec agentproto.DecoySpec) agentproto.DecoySpec {
	spec.Mode, spec.Template, spec.ProxyURL = "", "", ""
	return spec
}

func (h *decoyHost) reconcile() {
	h.mu.Lock()
	spec, applied, retryAt := h.spec, h.applied, h.retryAt
	h.mu.Unlock()
	h.helpers.Require(adguardName, spec.Mode == agentproto.DecoyAdGuard)

	if spec.Mode == agentproto.DecoyNone {
		h.stop()
		return
	}
	opts, held := h.optionsOf(spec)
	switch {
	case held != nil:
		// A listener that shows AdGuard Home must not outlive it: its port may be taken by whatever else runs here.
		if applied != nil && applied.Mode == agentproto.DecoyAdGuard {
			h.stop()
		}
		h.hold(held)
	case applied != nil && h.runner.IsRunning() && listenerOf(*applied) == listenerOf(spec):
		if *applied != spec {
			h.runner.Reload(opts)
			h.setApplied(&spec)
		}
	case h.now().Before(retryAt):
	default:
		h.start(spec, opts)
	}
}

// held is what keeps a decoy from being served yet: a program it needs that is not up, or that failed.
type held struct {
	why    string
	failed bool
}

// hold records what the start waits for, and leaves whatever is served until then as it is.
func (h *decoyHost) hold(by *held) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if by.failed {
		h.failure, h.waiting = by.why, ""
		return
	}
	h.failure, h.waiting = "", by.why
}

func (h *decoyHost) start(spec agentproto.DecoySpec, opts frontproxy.Options) {
	if h.runner.IsRunning() {
		_ = h.runner.Stop()
	}
	if err := h.runner.Start(opts); err != nil {
		logger.Warningf("agent: the decoy would not start: %v", err)
		h.mu.Lock()
		h.applied, h.failure, h.waiting, h.retryAt = nil, tailOf(err.Error()), "", h.now().Add(decoyRestartAfter)
		h.mu.Unlock()
		return
	}
	logger.Infof("agent: the decoy of %s is served on %s:%d", spec.Domain, decoyListen, spec.Port)
	h.setApplied(&spec)
}

func (h *decoyHost) setApplied(spec *agentproto.DecoySpec) {
	h.mu.Lock()
	h.applied, h.failure, h.waiting, h.retryAt = spec, "", "", time.Time{}
	h.mu.Unlock()
}

func (h *decoyHost) stop() {
	if h.runner.IsRunning() {
		if err := h.runner.Stop(); err != nil {
			logger.Warningf("agent: stopping the decoy: %v", err)
		}
	}
	h.setApplied(nil)
}

// optionsOf is what the listener is started with for spec. AdGuard Home has to be up first, so for it the
// answer may instead be what the decoy is held up by.
func (h *decoyHost) optionsOf(spec agentproto.DecoySpec) (frontproxy.Options, *held) {
	decoy := frontproxy.DecoyConfig{Mode: frontproxy.DecoyTemplate, Template: spec.Template, PublicOnly: !h.privateProxy, Seed: h.seed}
	switch spec.Mode {
	case agentproto.DecoyProxy:
		decoy.Mode, decoy.ProxyURL = frontproxy.DecoyProxy, spec.ProxyURL
	case agentproto.DecoyAdGuard:
		port, by := h.adguardUp()
		if by != nil {
			return frontproxy.Options{}, by
		}
		decoy.Mode, decoy.AdGuardPort = frontproxy.DecoyAdGuard, port
	}
	cert := frontproxy.CertAuto
	if spec.Cert == agentproto.DecoyCertManual {
		cert = frontproxy.CertManual
	}
	return frontproxy.Options{
		Listen:    decoyListen,
		Port:      spec.Port,
		DecoyOnly: true,
		Decoy:     decoy,
		TLS: frontproxy.TLSSettings{
			Mode: cert, Domain: spec.Domain, Email: spec.Email,
			CertFile: spec.CertFile, KeyFile: spec.KeyFile, StorageDir: h.storage,
		},
	}, nil
}

// adguardUp is the port AdGuard Home serves its page on once it runs, and what the decoy is held up by before.
func (h *decoyHost) adguardUp() (int, *held) {
	state, message := h.helpers.StateOf(adguardName)
	switch state {
	case agentproto.SidecarRunning:
		port, err := h.adguardPort()
		if err != nil {
			return 0, &held{why: tailOf("AdGuard Home: " + err.Error()), failed: true}
		}
		return port, nil
	case agentproto.SidecarFailed:
		return 0, &held{why: tailOf("AdGuard Home: " + message), failed: true}
	default:
		return 0, &held{why: "AdGuard Home: " + state}
	}
}
