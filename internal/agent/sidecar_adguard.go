package agent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/adguard"
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
	// The ports are free now and kept in its config, from which the decoy reads the one of the page: another
	// service of the server may hold a fixed one, and the decoy would show that to whoever probes it.
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

func (*adguardProgram) Start() error   { return adguard.GetManager().Start() }
func (*adguardProgram) Stop() error    { return adguard.GetManager().Stop() }
func (*adguardProgram) Running() bool  { return adguard.GetManager().IsRunning() }
func (*adguardProgram) Result() string { return adguard.GetManager().LastResult() }
