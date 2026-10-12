package agent

import (
	"net"
	"strconv"
	"testing"
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
