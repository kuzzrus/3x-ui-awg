package agent

import (
	"errors"
	"net"
	"os"
	"strconv"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/adguard"
)

// The ports go into the config of AdGuard Home for good, and the decoy shows whatever answers on the one of
// its page, so they must be free when they are chosen, for TCP and, for DNS, UDP as well.
func TestFreeLoopbackPortsAreFreeAndApart(t *testing.T) {
	web, dns, err := freeLoopbackPorts()
	if err != nil {
		t.Fatal(err)
	}
	if web == dns || web == 0 || dns == 0 {
		t.Fatalf("ports %d and %d, want two different ones", web, dns)
	}
	for name, address := range map[string]string{
		"web over TCP": net.JoinHostPort("127.0.0.1", strconv.Itoa(web)),
		"dns over TCP": net.JoinHostPort("127.0.0.1", strconv.Itoa(dns)),
	} {
		ln, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("%s: the port was not free: %v", name, err)
		}
		ln.Close()
	}
	conn, err := net.ListenPacket("udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(dns)))
	if err != nil {
		t.Fatalf("dns over UDP: the port was not free: %v", err)
	}
	conn.Close()
}

func TestProbeLoopbackPortRefusesOneThatIsHeld(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	held := ln.Addr().(*net.TCPAddr).Port
	if _, err := probeLoopbackPort("tcp", &held); err == nil {
		t.Fatalf("port %d is held and was reported free", held)
	}
}

// seedAdGuard puts a config of AdGuard Home with two free ports where the agent looks for it.
func seedAdGuard(t *testing.T) (web, dns int) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	web, dns, err := freeLoopbackPorts()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adguard.Seed(adguard.SeedOptions{WebPort: web, DNSPort: dns}); err != nil {
		t.Fatal(err)
	}
	return web, dns
}

// holdPort takes a loopback port for the rest of the test, as a service that came after the install does.
func holdPort(t *testing.T, network string, port int) {
	t.Helper()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if network == "udp" {
		conn, err := net.ListenPacket("udp", address)
		if err != nil {
			t.Fatalf("holding %s %d: %v", network, port, err)
		}
		t.Cleanup(func() { conn.Close() })
		return
	}
	ln, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("holding %s %d: %v", network, port, err)
	}
	t.Cleanup(func() { ln.Close() })
}

func TestMoveBusyPortsLeavesAConfigWhosePortsAreFree(t *testing.T) {
	seedAdGuard(t)
	before, err := os.ReadFile(adguard.ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := moveBusyPorts(); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(adguard.ConfigPath()); string(after) != string(before) {
		t.Fatalf("the config changed though both ports were free:\n%s\nbecame\n%s", before, after)
	}
}

// The ports go into the config for good, so the one that something came to hold since has to be given up for
// another, whichever of the three sockets AdGuard Home needs it was.
func TestMoveBusyPortsGivesUpAPortSomethingHolds(t *testing.T) {
	for name, tc := range map[string]struct {
		network string
		page    bool
	}{
		"the page":     {"tcp", true},
		"DNS over TCP": {"tcp", false},
		"DNS over UDP": {"udp", false},
	} {
		t.Run(name, func(t *testing.T) {
			web, dns := seedAdGuard(t)
			held := dns
			if tc.page {
				held = web
			}
			holdPort(t, tc.network, held)

			if err := moveBusyPorts(); err != nil {
				t.Fatal(err)
			}
			movedWeb, err := adguard.WebPort()
			if err != nil {
				t.Fatal(err)
			}
			movedDNS, err := adguard.DNSPort()
			if err != nil {
				t.Fatal(err)
			}
			if movedWeb == web && tc.page || movedDNS == dns && !tc.page {
				t.Errorf("the port %d that is held is still in the config (web %d, DNS %d)", held, movedWeb, movedDNS)
			}
			if movedWeb == movedDNS {
				t.Errorf("both ports are %d", movedWeb)
			}
			for _, free := range []struct {
				network string
				port    int
			}{{"tcp", movedWeb}, {"tcp", movedDNS}, {"udp", movedDNS}} {
				if _, err := probeLoopbackPort(free.network, &free.port); err != nil {
					t.Errorf("%s port %d of the config is not free: %v", free.network, free.port, err)
				}
			}
		})
	}
}

// What a start that could not bind used to do for ever: AdGuard Home is started on ports that are free by then.
func TestAdGuardProgramStartsOnPortsThatAreFree(t *testing.T) {
	web, _ := seedAdGuard(t)
	holdPort(t, "tcp", web)
	failure := errors.New("the start went no further")
	var startedOn int
	program := &adguardProgram{start: func() error {
		port, err := adguard.WebPort()
		if err != nil {
			return err
		}
		startedOn = port
		return failure
	}}

	if err := program.Start(); !errors.Is(err, failure) {
		t.Fatalf("Start returned %v, want what the start itself said", err)
	}
	if startedOn == 0 || startedOn == web {
		t.Fatalf("AdGuard Home was started on port %d, which is the one that is held (%d) or none", startedOn, web)
	}
}
