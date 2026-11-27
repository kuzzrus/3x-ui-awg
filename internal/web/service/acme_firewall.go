package service

import (
	"context"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction"
	"github.com/mhsanaei/3x-ui/v3/internal/hostfw"
)

// AcmeOpener lets a port through the host's firewall for a certificate round, and takes out what a round left behind.
type AcmeOpener interface {
	// Open lets the port through and returns the func that takes the hole out again, which is never nil.
	Open(ctx context.Context, port int) (release func(), err error)
	// Sweep takes out the rules that nothing holds open any more.
	Sweep(ctx context.Context)
}

var (
	acmeFirewallMu sync.Mutex
	acmeFirewall   AcmeOpener
)

// AcmeFirewall is the one AcmeOpener of the panel, shared by the reverse proxy that asks for it and by the sweep that takes out
// what a panel that stopped in the middle of a round left behind.
func AcmeFirewall() AcmeOpener {
	acmeFirewallMu.Lock()
	defer acmeFirewallMu.Unlock()
	if acmeFirewall == nil {
		acmeFirewall = hostfw.NewOpener(hostActionExec)
	}
	return acmeFirewall
}

// SetAcmeFirewall replaces the shared AcmeOpener, so that a test can put its own under it; nil brings the real one back.
func SetAcmeFirewall(o AcmeOpener) {
	acmeFirewallMu.Lock()
	defer acmeFirewallMu.Unlock()
	acmeFirewall = o
}

// hostActionExec makes a change of the firewall outside the sandbox of the panel's unit, which keeps /etc read-only: ufw writes its
// rules there.
func hostActionExec(ctx context.Context, name string, argv, env []string) ([]byte, error) {
	res, err := hostaction.Run(ctx, hostaction.Spec{Name: name, Argv: argv, Env: env})
	return res.Stdout, err
}
