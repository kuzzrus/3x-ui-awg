package frontproxy

import (
	"context"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// PortOpener lets a port through the firewall of the host for a while.
type PortOpener interface {
	// Open lets the port through and returns the func that takes the hole out again, which is never nil. The error says why
	// nothing was done; it is for the log, and the certificate round goes on without the hole.
	Open(ctx context.Context, port int) (release func(), err error)
}

// challengePort is where the HTTP-01 challenge is answered: Xray owns 443, so a TLS-ALPN challenge could never reach this process.
const challengePort = 80

// openTimeout bounds the wait for the firewall to let the port through, which a certificate attempt waits for.
const openTimeout = 25 * time.Second

// firewallRound keeps the challenge port open from the start of an attempt at a certificate to its end.
type firewallRound struct {
	opener  PortOpener
	mu      sync.Mutex
	release func()
}

func newFirewallRound(opener PortOpener) *firewallRound { return &firewallRound{opener: opener} }

// begin lets the port through. An attempt that never said it ended is ended first, so that no hole is left open behind it.
func (r *firewallRound) begin() {
	if r == nil || r.opener == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endLocked()
	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()
	release, err := r.opener.Open(ctx, challengePort)
	if err != nil {
		logger.Warningf("frontproxy: cannot let %d/tcp through the firewall for the certificate: %v", challengePort, err)
	}
	r.release = release
}

// end takes the hole out. It is safe to call when none is open.
func (r *firewallRound) end() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endLocked()
}

func (r *firewallRound) endLocked() {
	if r.release != nil {
		r.release()
		r.release = nil
	}
}

var (
	activeRoundMu sync.Mutex
	activeRound   *firewallRound
)

// setActiveRound remembers the round of the reverse proxy that runs, which a process has at most one of, so that Stop can end it.
func setActiveRound(r *firewallRound) {
	activeRoundMu.Lock()
	activeRound = r
	activeRoundMu.Unlock()
}

// endActiveRound takes out the hole that the running round holds open, if any.
func endActiveRound() {
	activeRoundMu.Lock()
	r := activeRound
	activeRoundMu.Unlock()
	r.end()
}
