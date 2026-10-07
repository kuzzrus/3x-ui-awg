package agent

import (
	"context"
	"net"
	"os"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Options is everything the agent is told when it starts.
type Options struct {
	Bundle   *agentproto.Bundle
	StateDir string
	LogDir   string
}

// Run serves the master until ctx ends. The core starts from the last good config at
// once, so a reboot with the master unreachable does not take the node offline.
func Run(ctx context.Context, opts Options) error {
	tlsConfig, err := opts.Bundle.ServerTLSConfig()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(opts.LogDir, 0o750); err != nil {
		return err
	}
	state, err := OpenState(opts.StateDir)
	if err != nil {
		return err
	}
	core := NewCore(state, opts.LogDir)
	defer core.Close()
	server, err := NewServer(core, state, opts.Bundle.Secret)
	if err != nil {
		return err
	}

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", net.JoinHostPort("", strconv.Itoa(opts.Bundle.Port)))
	if err != nil {
		return err
	}
	// The port is open before the core starts: a stored config that will not start must not
	// keep the master out for the whole start-up wait. A push made meanwhile waits its turn.
	ctx, stop := context.WithCancel(ctx)
	supervised := make(chan struct{})
	go func() {
		defer close(supervised)
		if err := core.Boot(ctx); err != nil {
			logger.Warning("agent: the last good config did not start, waiting for the master:", err)
		}
		core.Run(ctx)
	}()
	defer func() {
		stop()
		<-supervised
	}()

	logger.Info("agent: serving the master on", ln.Addr())
	return server.Serve(ctx, ln, tlsConfig)
}
