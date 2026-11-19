package amneziawgnet

import (
	"context"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// ipv4Packet is a whole IPv4/UDP packet with a valid header checksum.
func ipv4Packet(src, dst string, ttl uint8, payload string) []byte {
	pkt := make([]byte, header.IPv4MinimumSize+len(payload))
	ip := header.IPv4(pkt)
	ip.Encode(&header.IPv4Fields{
		TotalLength: uint16(len(pkt)),
		TTL:         ttl,
		Protocol:    uint8(header.UDPProtocolNumber),
		SrcAddr:     tcpip.AddrFromSlice(netip.MustParseAddr(src).AsSlice()),
		DstAddr:     tcpip.AddrFromSlice(netip.MustParseAddr(dst).AsSlice()),
	})
	ip.SetChecksum(^ip.CalculateChecksum())
	copy(pkt[header.IPv4MinimumSize:], payload)
	return pkt
}

func ipv6Packet(src, dst string, hopLimit uint8, payload string) []byte {
	pkt := make([]byte, header.IPv6MinimumSize+len(payload))
	header.IPv6(pkt).Encode(&header.IPv6Fields{
		PayloadLength:     uint16(len(payload)),
		TransportProtocol: header.UDPProtocolNumber,
		HopLimit:          hopLimit,
		SrcAddr:           tcpip.AddrFromSlice(netip.MustParseAddr(src).AsSlice()),
		DstAddr:           tcpip.AddrFromSlice(netip.MustParseAddr(dst).AsSlice()),
	})
	copy(pkt[header.IPv6MinimumSize:], payload)
	return pkt
}

// routedPeers is a server with peer a at .2, and peer b at .3 with a network behind it, for both families.
func routedPeers() *PeerIndex {
	return NewPeerIndex([]amneziawg.Peer{
		{PublicKey: "a", Email: "a@x", AllowedIPs: []string{"10.9.0.2/32", "fd00:9::2/128"}},
		{PublicKey: "b", Email: "b@x", AllowedIPs: []string{"10.9.0.3/32", "192.168.50.0/24", "fd00:9::3/128", "fd00:50::/64"}},
	})
}

func newRoutedTun(t *testing.T, routing bool) *stackTun {
	t.Helper()
	tun, _, err := createNetTUNWithStack([]netip.Addr{netip.MustParseAddr("10.9.0.1"), netip.MustParseAddr("fd00:9::1")}, 1420)
	if err != nil {
		t.Fatalf("createNetTUNWithStack: %v", err)
	}
	t.Cleanup(func() { tun.Close() })
	st := tun.(*stackTun)
	if routing {
		st.peerRoute.Store(&peerRoute{peers: routedPeers()})
	}
	return st
}

// sent is what the tun hands to the device to encrypt: the packet that was forwarded, or nothing.
func sent(st *stackTun) []byte {
	select {
	case v := <-st.incomingPacket:
		defer v.Release()
		out := make([]byte, v.Size())
		_, _ = v.Read(out)
		return out
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

func TestPeerRouteSendsAPacketToTheOwnerOfItsDestination(t *testing.T) {
	cases := []struct {
		name, dst string
		packet    func(dst string) []byte
	}{
		{"a peer's address", "10.9.0.3", func(d string) []byte { return ipv4Packet("10.9.0.2", d, 64, "hello") }},
		{"a network behind a peer", "192.168.50.7", func(d string) []byte { return ipv4Packet("10.9.0.2", d, 64, "hello") }},
		{"a peer's IPv6 address", "fd00:9::3", func(d string) []byte { return ipv6Packet("fd00:9::2", d, 64, "hello") }},
		{"an IPv6 network behind a peer", "fd00:50::7", func(d string) []byte { return ipv6Packet("fd00:9::2", d, 64, "hello") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newRoutedTun(t, true)
			in := c.packet(c.dst)
			if _, err := st.Write([][]byte{append([]byte(nil), in...)}, 0); err != nil {
				t.Fatalf("Write: %v", err)
			}
			out := sent(st)
			if out == nil {
				t.Fatal("nothing was handed back to the device for the peer that owns the destination")
			}
			if len(out) != len(in) {
				t.Fatalf("forwarded %d bytes, want %d", len(out), len(in))
			}
			if string(out[len(out)-5:]) != "hello" {
				t.Errorf("the payload changed on the way: %q", out[len(out)-5:])
			}
			if in[0]>>4 == 4 {
				ip := header.IPv4(out)
				if ip.TTL() != 63 {
					t.Errorf("TTL = %d, want 63: a router takes a hop", ip.TTL())
				}
				if !ip.IsChecksumValid() {
					t.Error("the header checksum is wrong after the hop was taken")
				}
			} else if hl := header.IPv6(out).HopLimit(); hl != 63 {
				t.Errorf("hop limit = %d, want 63", hl)
			}
		})
	}
}

func TestPeerRouteLeavesTheRestToTheStack(t *testing.T) {
	// A peer that owns every address, as one that is the way out for the others does: an address that is no one's still stays out.
	catchAll := NewPeerIndex([]amneziawg.Peer{{PublicKey: "x", AllowedIPs: []string{"0.0.0.0/0", "::/0"}}})
	halves := NewPeerIndex([]amneziawg.Peer{{PublicKey: "x", AllowedIPs: []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}}})
	// Prefixes that do contain a multicast or the unspecified address, and are not default routes.
	special := NewPeerIndex([]amneziawg.Peer{{PublicKey: "x", AllowedIPs: []string{"224.0.0.0/24", "ff00::/8", "0.0.0.0/8", "::/8"}}})
	cases := []struct {
		name    string
		routing bool
		peers   *PeerIndex // nil: the peers of routedPeers
		packet  []byte
	}{
		{"routing is off (the default)", false, nil, ipv4Packet("10.9.0.2", "10.9.0.3", 64, "hello")},
		{"a destination that no peer owns", true, nil, ipv4Packet("10.9.0.2", "8.8.8.8", 64, "hello")},
		{"the server's own address", true, nil, ipv4Packet("10.9.0.2", "10.9.0.1", 64, "hello")},
		{"a multicast destination", true, nil, ipv4Packet("10.9.0.2", "224.0.0.251", 64, "hello")},
		{"a multicast destination of a peer that lists the multicast range", true, special, ipv4Packet("10.9.0.2", "224.0.0.251", 64, "hello")},
		{"an IPv6 multicast destination of a peer that lists the multicast range", true, special, ipv6Packet("fd00:9::2", "ff02::fb", 64, "hello")},
		{"the unspecified address of a peer that lists its range", true, special, ipv4Packet("10.9.0.2", "0.0.0.0", 64, "hello")},
		{"the IPv6 unspecified address of a peer that lists its range", true, special, ipv6Packet("fd00:9::2", "::", 64, "hello")},
		{"a packet that is cut short", true, nil, ipv4Packet("10.9.0.2", "10.9.0.3", 64, "hello")[:22]},
		// A default route claims no network: the other clients' way to the internet must not turn into a way to that peer.
		{"an address that only a default route owns", true, catchAll, ipv4Packet("10.9.0.2", "8.8.8.8", 64, "hello")},
		{"an IPv6 address that only a default route owns", true, catchAll, ipv6Packet("fd00:9::2", "2001:4860:4860::8888", 64, "hello")},
		{"an address of the lower half of the internet", true, halves, ipv4Packet("10.9.0.2", "8.8.8.8", 64, "hello")},
		{"an address of the upper half of the internet", true, halves, ipv4Packet("10.9.0.2", "200.1.1.1", 64, "hello")},
		{"an IPv6 address of the half of the internet", true, halves, ipv6Packet("fd00:9::2", "2001:4860:4860::8888", 64, "hello")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newRoutedTun(t, c.routing)
			if c.peers != nil {
				st.peerRoute.Store(&peerRoute{peers: c.peers})
			}
			if st.forwardToPeer(append([]byte(nil), c.packet...)) {
				t.Error("forwardToPeer took a packet that is not for a peer")
			}
			if out := sent(st); out != nil {
				t.Errorf("a packet was handed to the device: % x", out[:20])
			}
		})
	}
}

func TestPeerRouteDropsWhatCannotBeForwarded(t *testing.T) {
	cases := []struct {
		name   string
		packet []byte
	}{
		{"a packet for the range of the peer that sent it", ipv4Packet("10.9.0.3", "192.168.50.7", 64, "hello")},
		{"a packet with no hop left (IPv4)", ipv4Packet("10.9.0.2", "10.9.0.3", 1, "hello")},
		{"a packet with no hop left (IPv6)", ipv6Packet("fd00:9::2", "fd00:9::3", 1, "hello")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newRoutedTun(t, true)
			if !st.forwardToPeer(append([]byte(nil), c.packet...)) {
				t.Error("forwardToPeer left the packet to the stack, which would send it on to the host's network")
			}
			if out := sent(st); out != nil {
				t.Errorf("a packet that has to be dropped was handed to the device: % x", out[:20])
			}
		})
	}
}

// triTunnel is a server and two clients over host UDP: a at .2, and b at .3 with a host (192.168.50.7) behind it.
type triTunnel struct {
	server, a, b *Device
}

func newTriTunnelForTest(t *testing.T, routing bool) *triTunnel {
	t.Helper()
	serverPriv, serverPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	aPriv, aPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	bPriv, bPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatal(err)
	}
	port := freeUDPPort(t)
	obf := amneziawg.Obfuscation20{Jc: 4, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20}
	serverInst := amneziawg.Instance{
		Id: 7, InterfaceName: "awg-peerroute", ListenPort: port, PrivateKey: serverPriv, PublicKey: serverPub,
		Address: []string{"10.204.0.1/24"}, MTU: 1420, Obfuscation: obf,
		Peers: []amneziawg.Peer{
			{PublicKey: aPub, Email: "a@x", AllowedIPs: []string{"10.204.0.2/32"}},
			{PublicKey: bPub, Email: "b@x", AllowedIPs: []string{"10.204.0.3/32", "192.168.50.0/24"}},
		},
	}
	slog := verboseLoggerForTest("(prsrv) ")
	server, err := newUnconfiguredDevice(serverInst, DeviceOptions{Logger: slog})
	if err != nil {
		t.Fatalf("server device: %v", err)
	}
	t.Cleanup(server.Close)
	if routing {
		server.SetPeerRouting(NewPeerIndex(serverInst.Peers))
	}
	if err := server.Configure(serverInst, DeviceOptions{Logger: slog}); err != nil {
		t.Fatalf("server Configure: %v", err)
	}

	client := func(tag, priv string, addrs []string) *Device {
		inst := amneziawg.OutboundInstance{
			Tag: tag, Address: addrs, MTU: 1420, PrivateKey: priv, Obfuscation: obf,
			Peers: []amneziawg.OutboundPeer{{
				PublicKey: serverPub, Endpoint: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
				AllowedIPs: []string{"0.0.0.0/0"}, KeepAlive: 1,
			}},
		}
		log := verboseLoggerForTest("(" + tag + ") ")
		d, err := newUnconfiguredClientDevice(inst, DeviceOptions{Logger: log})
		if err != nil {
			t.Fatalf("%s device: %v", tag, err)
		}
		if err := d.ConfigureClient(inst, DeviceOptions{Logger: log}); err != nil {
			t.Fatalf("%s ConfigureClient: %v", tag, err)
		}
		t.Cleanup(d.Close)
		return d
	}
	tri := &triTunnel{
		server: server,
		a:      client("pra", aPriv, []string{"10.204.0.2/32"}),
		b:      client("prb", bPriv, []string{"10.204.0.3/32", "192.168.50.7/32"}),
	}
	waitHandshakes(t, server, 2)
	return tri
}

// waitHandshakes waits until n of the server's peers have completed a handshake.
func waitHandshakes(t *testing.T, server *Device, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		dump, err := server.IpcGet()
		if err != nil {
			t.Fatalf("IpcGet: %v", err)
		}
		done := 0
		for _, line := range strings.Split(dump, "\n") {
			if v, ok := strings.CutPrefix(line, "last_handshake_time_sec="); ok && v != "0" {
				done++
			}
		}
		if done >= n {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("fewer than %d peers completed a handshake", n)
}

func tcpAddr(addr string, port int) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFromSlice(netip.MustParseAddr(addr).AsSlice()), Port: uint16(port)}
}

// echoOnce listens in d's stack and echoes whatever the first connection sends.
func echoOnce(t *testing.T, d *Device, addr string, port int) {
	t.Helper()
	ln, err := gonet.ListenTCP(d.Stack, tcpAddr(addr, port), ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("listen %s:%d: %v", addr, port, err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = io.Copy(c, c)
	}()
}

func dialEcho(ctx context.Context, from *Device, addr string, port int) error {
	c, err := gonet.DialContextTCP(ctx, from.Stack, tcpAddr(addr, port), ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("hello through the server")); err != nil {
		return err
	}
	buf := make([]byte, len("hello through the server"))
	if _, err := io.ReadFull(c, buf); err != nil {
		return err
	}
	if string(buf) != "hello through the server" {
		return io.ErrUnexpectedEOF
	}
	return nil
}

func TestPeersReachEachOtherThroughTheServerWhenItRoutes(t *testing.T) {
	tri := newTriTunnelForTest(t, true)
	echoOnce(t, tri.b, "10.204.0.3", 8080)
	echoOnce(t, tri.b, "192.168.50.7", 9090)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dialEcho(ctx, tri.a, "10.204.0.3", 8080); err != nil {
		t.Errorf("a -> b's tunnel address over TCP: %v", err)
	}
	if err := dialEcho(ctx, tri.a, "192.168.50.7", 9090); err != nil {
		t.Errorf("a -> a host behind b over TCP: %v", err)
	}
}

func TestPeersReachEachOtherOverUDPThroughTheServerWhenItRoutes(t *testing.T) {
	tri := newTriTunnelForTest(t, true)
	bAddr := tcpAddr("10.204.0.3", 5353)
	srv, err := gonet.DialUDP(tri.b.Stack, &bAddr, nil, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("bind udp on b: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	go func() {
		buf := make([]byte, 512)
		for {
			n, from, err := srv.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = srv.WriteTo(append([]byte("echo:"), buf[:n]...), from)
		}
	}()
	aAddr := tcpAddr("10.204.0.2", 0)
	cli, err := gonet.DialUDP(tri.a.Stack, &aAddr, &bAddr, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatalf("dial udp from a: %v", err)
	}
	defer cli.Close()
	_ = cli.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := cli.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 64)
	n, err := cli.Read(buf)
	if err != nil {
		t.Fatalf("a never got b's reply: %v", err)
	}
	if string(buf[:n]) != "echo:ping" {
		t.Errorf("reply = %q, want %q", buf[:n], "echo:ping")
	}
}

// Without the setting a server keeps its peers apart, as it always did.
func TestPeersCannotReachEachOtherWhenTheServerDoesNotRoute(t *testing.T) {
	tri := newTriTunnelForTest(t, false)
	echoOnce(t, tri.b, "10.204.0.3", 8080)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := dialEcho(ctx, tri.a, "10.204.0.3", 8080); err == nil {
		t.Error("a reached b although the server does not route between its peers")
	}
}

func carried(st *stackTun) map[string]RoutedTraffic {
	out := map[string]RoutedTraffic{}
	for _, r := range st.routed.take() {
		out[r.Email] = r
	}
	return out
}

func TestPeerRouteCountsWhatItCarriesForBothClients(t *testing.T) {
	st := newRoutedTun(t, true)
	packet := ipv4Packet("10.9.0.2", "10.9.0.3", 64, "hello")
	if _, err := st.Write([][]byte{packet}, 0); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got := carried(st)
	size := int64(len(packet))
	if a := got["a@x"]; a.Up != size || a.Down != 0 {
		t.Errorf("a = %+v, want it to have sent %d bytes and received none", a, size)
	}
	if b := got["b@x"]; b.Down != size || b.Up != 0 {
		t.Errorf("b = %+v, want it to have received %d bytes and sent none", b, size)
	}
	if again := carried(st); len(again) != 0 {
		t.Errorf("a second take = %+v, want nothing: counting starts from nothing", again)
	}
}

func TestPeerRouteDoesNotCountWhatItDroppedOrLeftToTheStack(t *testing.T) {
	st := newRoutedTun(t, true)
	for _, p := range [][]byte{
		ipv4Packet("10.9.0.3", "192.168.50.7", 64, "hello"), // for the range of the sender
		ipv4Packet("10.9.0.2", "10.9.0.3", 1, "hello"),      // no hop left
		ipv4Packet("10.9.0.2", "8.8.8.8", 64, "hello"),      // no peer's
	} {
		st.forwardToPeer(append([]byte(nil), p...))
	}
	if got := carried(st); len(got) != 0 {
		t.Errorf("counted %+v for packets that were not carried between peers", got)
	}
}

// The size is the packet's as it was received: a hop taken changes a byte, not the length.
func TestPeerRouteCountsTheBytesOfEveryPacket(t *testing.T) {
	st := newRoutedTun(t, true)
	total := int64(0)
	for _, payload := range []string{"x", "hello", "a longer payload than the others"} {
		p := ipv4Packet("10.9.0.2", "192.168.50.7", 64, payload)
		total += int64(len(p))
		st.forwardToPeer(p)
	}
	if got := carried(st); got["a@x"].Up != total || got["b@x"].Down != total {
		t.Errorf("a.Up = %d, b.Down = %d, want both %d", got["a@x"].Up, got["b@x"].Down, total)
	}
}

func TestPeerRouteDoesNotCountForAPeerWithoutAnEmail(t *testing.T) {
	st := newRoutedTun(t, false)
	st.peerRoute.Store(&peerRoute{peers: NewPeerIndex([]amneziawg.Peer{
		{PublicKey: "a", AllowedIPs: []string{"10.9.0.2/32"}},
		{PublicKey: "b", Email: "b@x", AllowedIPs: []string{"10.9.0.3/32"}},
	})})
	st.forwardToPeer(ipv4Packet("10.9.0.2", "10.9.0.3", 64, "hello"))
	got := carried(st)
	if len(got) != 1 || got["b@x"].Down == 0 {
		t.Errorf("counted %+v, want only b's download: a has no email to charge", got)
	}
}

// A packet that the queue to the device refused went nowhere, so it is charged to no one.
func TestPeerRouteDoesNotCountAPacketThatTheQueueRefused(t *testing.T) {
	st := newRoutedTun(t, true)
	for range tunQueueDepth {
		st.incomingPacket <- buffer.NewViewWithData([]byte{0})
	}
	if !st.forwardToPeer(ipv4Packet("10.9.0.2", "10.9.0.3", 64, "hello")) {
		t.Fatal("forwardToPeer left a packet for a peer to the stack")
	}
	if got := carried(st); len(got) != 0 {
		t.Errorf("counted %+v for a packet that was dropped", got)
	}
}

// Every packet is one client's upload and another's download: the two sides always add up to the same.
func TestPeersRoutedByTheServerAreCountedOnBothSides(t *testing.T) {
	tri := newTriTunnelForTest(t, true)
	echoOnce(t, tri.b, "10.204.0.3", 8080)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := dialEcho(ctx, tri.a, "10.204.0.3", 8080); err != nil {
		t.Fatalf("a -> b over TCP: %v", err)
	}

	got := map[string]RoutedTraffic{}
	for _, r := range tri.server.TakeRoutedTraffic() {
		got[r.Email] = r
	}
	a, b := got["a@x"], got["b@x"]
	if a.Up == 0 || a.Down == 0 || b.Up == 0 || b.Down == 0 {
		t.Errorf("a = %+v, b = %+v, want both to have sent and received: the echo went both ways", a, b)
	}
	if a.Up+b.Up != a.Down+b.Down {
		t.Errorf("sent %d bytes and received %d: every packet is one client's upload and another's download", a.Up+b.Up, a.Down+b.Down)
	}
}
