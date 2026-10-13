package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/adguard"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// adguardName is the program that the decoy runs when it is AdGuard Home. It is not one the master lists,
	// so it is no name of the protocol.
	adguardName = "adguard"
	// adguardDownloadTimeout bounds the download of the release, which is some tens of MiB.
	adguardDownloadTimeout = 15 * time.Minute
)

// adguardProgram is the AdGuard Home of internal/adguard, run on the loopback for the decoy to show.
type adguardProgram struct {
	client *http.Client // the download client, unless a test sets one
	start  func() error // starts AdGuard Home, unless a test sets one
}

func (*adguardProgram) Installed() bool { return adguard.IsInstalled() && adguard.IsConfigured() }

func (p *adguardProgram) Install(ctx context.Context) error {
	client := p.client
	if client == nil {
		client = &http.Client{Timeout: adguardDownloadTimeout}
	}
	if err := adguard.Install(ctx, client); err != nil {
		return err
	}
	// The ports are free now and kept in its config, from which the decoy reads the one of the page: a fixed one may
	// belong to another service, which the decoy would show. Start chooses again when something comes to hold one.
	webPort, dnsPort, err := freeLoopbackPorts()
	if err != nil {
		return err
	}
	// The password it makes is dropped: nobody is to sign in to a decoy, and a visitor who tries finds a login page.
	_, err = adguard.Seed(adguard.SeedOptions{WebPort: webPort, DNSPort: dnsPort})
	return err
}

// freeLoopbackPorts finds two loopback ports that nothing holds: one for a web page, and one that is free
// for DNS over both TCP and UDP.
func freeLoopbackPorts() (web, dns int, err error) {
	if web, err = probeLoopbackPort("tcp", nil); err != nil {
		return 0, 0, err
	}
	for range 20 {
		if dns, err = probeLoopbackPort("tcp", nil); err != nil {
			return 0, 0, err
		}
		if _, err := probeLoopbackPort("udp", &dns); err == nil && dns != web {
			return web, dns, nil
		}
	}
	return 0, 0, fmt.Errorf("no loopback port is free for the DNS of AdGuard Home")
}

// probeLoopbackPort is a port that nothing holds on the loopback now, or the one asked for, when it is free.
func probeLoopbackPort(network string, want *int) (int, error) {
	address := "127.0.0.1:0"
	if want != nil {
		address = net.JoinHostPort("127.0.0.1", strconv.Itoa(*want))
	}
	var listen net.ListenConfig
	if network == "udp" {
		conn, err := listen.ListenPacket(context.Background(), "udp", address)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		return conn.LocalAddr().(*net.UDPAddr).Port, nil
	}
	ln, err := listen.Listen(context.Background(), "tcp", address)
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func (p *adguardProgram) Start() error {
	if err := moveBusyPorts(); err != nil {
		return err
	}
	if p.start != nil {
		return p.start()
	}
	return adguard.GetManager().Start()
}

// moveBusyPorts gives AdGuard Home other ports when something holds one of its two, which were only free when they
// were chosen. It is not running here, so its config can be rewritten.
func moveBusyPorts() error {
	web, err := adguard.WebPort()
	if err != nil {
		return err
	}
	dns, err := adguard.DNSPort()
	if err != nil {
		return err
	}
	_, webErr := probeLoopbackPort("tcp", &web)
	_, dnsTCPErr := probeLoopbackPort("tcp", &dns)
	_, dnsUDPErr := probeLoopbackPort("udp", &dns)
	if webErr == nil && dnsTCPErr == nil && dnsUDPErr == nil {
		return nil
	}
	moved, movedDNS, err := freeLoopbackPorts()
	if err != nil {
		return err
	}
	logger.Warningf("agent: a port of AdGuard Home is held by something else, moving it from %d and %d to %d and %d", web, dns, moved, movedDNS)
	return adguard.SetPorts(moved, movedDNS)
}

func (*adguardProgram) Stop() error    { return adguard.GetManager().Stop() }
func (*adguardProgram) Running() bool  { return adguard.GetManager().IsRunning() }
func (*adguardProgram) Result() string { return adguard.GetManager().LastResult() }
