package agent

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

const (
	// psiphonDownloadTimeout bounds the download of the pinned binary, which is about 10 MiB.
	psiphonDownloadTimeout = 10 * time.Minute
	// tunnelSummaryEvery keeps the scan of the notice log, which grows for as long as the process
	// runs, to one in a while, whatever the rate of the status requests.
	tunnelSummaryEvery = 30 * time.Second
)

// psiphonProgram is the Psiphon client of internal/psiphon, a SOCKS proxy on psiphon.SocksPort of the
// loopback, run with the config this build bundles.
type psiphonProgram struct {
	tunnel tunnelSummary
	client *http.Client // the download client, unless a test sets one
}

// Installed asks for the binary this build pins, so that one an earlier build downloaded is fetched again,
// and for the config, which Install seeds and Psiphon does not start without.
func (*psiphonProgram) Installed() bool {
	return psiphon.IsInstalled() && psiphon.MatchesPin() && psiphon.IsConfigured()
}

func (p *psiphonProgram) Install(ctx context.Context) error {
	// psiphon.Install keeps a binary that is there, so one that is not the pinned is put aside first.
	if psiphon.IsInstalled() && !psiphon.MatchesPin() {
		if err := os.Remove(psiphon.BinPath()); err != nil {
			return err
		}
	}
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: psiphonDownloadTimeout}
	}
	return psiphon.Install(ctx, client)
}

func (p *psiphonProgram) Start() error {
	p.tunnel.forget()
	return psiphon.GetManager().Start()
}

func (*psiphonProgram) Stop() error   { return psiphon.GetManager().Stop() }
func (*psiphonProgram) Running() bool { return psiphon.GetManager().IsRunning() }

// Result is how the tunnel stands while the process runs, since the process is up long before it is,
// and what the process said last once it has ended.
func (p *psiphonProgram) Result() string {
	manager := psiphon.GetManager()
	if manager.IsRunning() {
		if text, err := p.tunnel.get(time.Now(), psiphon.CurrentTunnel); err == nil {
			return text
		}
	}
	return manager.LastResult()
}

// tunnelSummary is how the tunnel stands in a few words, read again only when it is old.
type tunnelSummary struct {
	mu   sync.Mutex
	at   time.Time
	text string
}

func (s *tunnelSummary) get(now time.Time, read func() (psiphon.TunnelStatus, error)) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.at.IsZero() || now.Sub(s.at) >= tunnelSummaryEvery {
		tunnel, err := read()
		if err != nil {
			return "", err
		}
		s.text, s.at = describeTunnel(tunnel), now
	}
	return s.text, nil
}

// forget drops what was read, which a new process makes untrue.
func (s *tunnelSummary) forget() {
	s.mu.Lock()
	s.at = time.Time{}
	s.mu.Unlock()
}

func describeTunnel(tunnel psiphon.TunnelStatus) string {
	switch {
	case !tunnel.Connected:
		return "connecting"
	case tunnel.ServerRegion != "":
		return fmt.Sprintf("tunnel up, server in %s", tunnel.ServerRegion)
	default:
		return "tunnel up"
	}
}
