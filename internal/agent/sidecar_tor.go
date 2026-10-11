package agent

import (
	"context"
	"os/exec"
	"regexp"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/tor"
)

// systemTorUnits are the units a package of Tor starts for the system, one of which is the daemon.
var systemTorUnits = []string{"tor@default.service", "tor.service"}

// torLinePrefix is the date, and the level when it is only a notice, that Tor puts before a line of its log.
var torLinePrefix = regexp.MustCompile(`^[A-Z][a-z]{2} +\d+ \d\d:\d\d:\d\d(?:\.\d+)? (?:\[notice\] )?`)

// torProgram is the Tor client of internal/tor, a SOCKS proxy on tor.SocksPort of the loopback.
type torProgram struct{}

func (torProgram) Installed() bool { return tor.IsAvailable() }

// Install leaves the package manager running when ctx ends: killing it half-way through a package
// would leave the package database unfinished.
func (torProgram) Install(ctx context.Context) error {
	done := make(chan error, 1)
	go func() {
		err := tor.Install()
		if err == nil {
			quietSystemTor()
		}
		done <- err
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// quietSystemTor stops and disables the Tor service that installing the package has just started for the
// system. Nothing here uses it, and it would run beside the agent's own Tor for as long as the server is up.
func quietSystemTor() {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return
	}
	for _, unit := range systemTorUnits {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, systemctl, "disable", "--now", unit).CombinedOutput()
		cancel()
		if err != nil {
			logger.Debugf("agent: systemctl disable --now %s: %v: %s", unit, err, out)
		}
	}
}

func (torProgram) Start() error  { return tor.GetManager().Start() }
func (torProgram) Stop() error   { return tor.GetManager().Stop() }
func (torProgram) Running() bool { return tor.GetManager().IsRunning() }

func (torProgram) Result() string { return tidyTorLine(tor.GetManager().LastResult()) }

// tidyTorLine is a line of Tor's log without the date, which the panel shows beside the state.
func tidyTorLine(line string) string { return torLinePrefix.ReplaceAllString(line, "") }

func defaultSidecars() map[string]sidecar {
	return map[string]sidecar{agentproto.SidecarTor: torProgram{}}
}
