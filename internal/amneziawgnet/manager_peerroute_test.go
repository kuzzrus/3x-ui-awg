package amneziawgnet

import (
	"net/netip"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

func routingPeer(t *testing.T, email, addr string) amneziawg.Peer {
	t.Helper()
	_, pub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return amneziawg.Peer{Email: email, PublicKey: pub, AllowedIPs: []string{addr}}
}

func routingInstance(t *testing.T, id int, subnet string, on bool, peers ...amneziawg.Peer) amneziawg.Instance {
	t.Helper()
	priv, pub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	return amneziawg.Instance{
		Id: id, InterfaceName: "awgroute", PrivateKey: priv, PublicKey: pub,
		Address: []string{subnet + ".1/24"}, MTU: 1420,
		Obfuscation:         amneziawg.Obfuscation20{Jc: 4, Jmin: 40, Jmax: 70, S1: 20, S2: 30, S3: 20, S4: 20},
		Peers:               peers,
		RouteBetweenClients: on,
	}
}

func newRoutingManager(t *testing.T) *Manager {
	t.Helper()
	m := &Manager{ifaces: map[int]*managed{}}
	t.Cleanup(m.StopAll)
	return m
}

func TestEnsureStartsADeviceThatRoutesBetweenClientsOnlyWhenAsked(t *testing.T) {
	m := newRoutingManager(t)
	peers := []amneziawg.Peer{routingPeer(t, "a@x", "10.211.0.2/32"), routingPeer(t, "b@x", "10.211.0.3/32")}
	for _, c := range []struct {
		id   int
		on   bool
		want bool
	}{{21, false, false}, {22, true, true}} {
		if err := m.Ensure(Desired{Instance: routingInstance(t, c.id, "10.211.0", c.on, peers...)}); err != nil {
			t.Fatalf("Ensure %d: %v", c.id, err)
		}
		dev, _, ok := m.Lookup(c.id)
		if !ok {
			t.Fatalf("Lookup %d: not found", c.id)
		}
		if got := dev.tun.peerRoute.Load() != nil; got != c.want {
			t.Errorf("inbound %d (routing %v): device routes between clients = %v, want %v", c.id, c.on, got, c.want)
		}
	}
}

// The switch is not part of the UAPI configuration, so flipping it renders the same one: it must still take effect, and without
// dropping the handshakes of every client.
func TestTogglingRoutingBetweenClientsTakesEffectWithoutRebuildingTheDevice(t *testing.T) {
	m := newRoutingManager(t)
	// One instance throughout, so that the switch is the only thing that differs between the calls.
	inst := routingInstance(t, 23, "10.211.0", false, routingPeer(t, "a@x", "10.211.0.2/32"))
	ensure := func(on bool) *Device {
		t.Helper()
		inst.RouteBetweenClients = on
		if err := m.Ensure(Desired{Instance: inst}); err != nil {
			t.Fatalf("Ensure (routing %v): %v", on, err)
		}
		dev, _, _ := m.Lookup(23)
		return dev
	}
	off := ensure(false)
	on := ensure(true)
	if off != on {
		t.Error("switching routing on rebuilt the device: every client would have to handshake again")
	}
	if on.tun.peerRoute.Load() == nil {
		t.Error("the device does not route between clients after the switch was turned on")
	}
	back := ensure(false)
	if back != on {
		t.Error("switching routing off rebuilt the device")
	}
	if back.tun.peerRoute.Load() != nil {
		t.Error("the device still routes between clients after the switch was turned off")
	}
}

// The routing reads the same peers as the handlers do: a client that is added or removed is reachable, or not, at once.
func TestRoutingBetweenClientsFollowsTheLivePeers(t *testing.T) {
	m := newRoutingManager(t)
	a := routingPeer(t, "a@x", "10.211.0.2/32")
	b := routingPeer(t, "b@x", "10.211.0.3/32")
	inst := routingInstance(t, 24, "10.211.0", true, a)
	if err := m.Ensure(Desired{Instance: inst}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	dev, _, _ := m.Lookup(24)
	reaches := func(addr string) bool {
		route := dev.tun.peerRoute.Load()
		if route == nil {
			return false
		}
		_, ok := route.peers.Lookup(netipAddr(t, addr))
		return ok
	}
	if reaches("10.211.0.3") {
		t.Fatal("10.211.0.3 is reachable before its client exists")
	}

	inst.Peers = []amneziawg.Peer{a, b}
	if err := m.Ensure(Desired{Instance: inst}); err != nil {
		t.Fatalf("Ensure with b: %v", err)
	}
	if !reaches("10.211.0.3") {
		t.Error("10.211.0.3 is not reachable after its client was added: the routing reads a stale peer list")
	}

	inst.Peers = []amneziawg.Peer{a}
	if err := m.Ensure(Desired{Instance: inst}); err != nil {
		t.Fatalf("Ensure without b: %v", err)
	}
	if reaches("10.211.0.3") {
		t.Error("10.211.0.3 is still reachable after its client was removed")
	}
}

func TestTakeRoutedTrafficGathersEveryInterfaceAndCountsFromNothing(t *testing.T) {
	m := newRoutingManager(t)
	for _, c := range []struct {
		id     int
		subnet string
		a, b   string
	}{{25, "10.211.0", "a@x", "b@x"}, {26, "10.212.0", "c@x", "d@x"}} {
		inst := routingInstance(t, c.id, c.subnet, true,
			routingPeer(t, c.a, c.subnet+".2/32"), routingPeer(t, c.b, c.subnet+".3/32"))
		if err := m.Ensure(Desired{Instance: inst}); err != nil {
			t.Fatalf("Ensure %d: %v", c.id, err)
		}
		dev, _, _ := m.Lookup(c.id)
		if !dev.tun.forwardToPeer(ipv4Packet(c.subnet+".2", c.subnet+".3", 64, "hello")) {
			t.Fatalf("inbound %d: the packet was not taken for the other peer", c.id)
		}
	}

	got := map[string]RoutedTraffic{}
	for _, r := range m.TakeRoutedTraffic() {
		got[r.Email] = r
	}
	for _, want := range []struct {
		email string
		up    bool
	}{{"a@x", true}, {"b@x", false}, {"c@x", true}, {"d@x", false}} {
		r, ok := got[want.email]
		if !ok {
			t.Errorf("nothing counted for %s: %+v", want.email, got)
			continue
		}
		if (want.up && r.Up == 0) || (!want.up && r.Down == 0) {
			t.Errorf("%s = %+v, want its own direction counted", want.email, r)
		}
	}
	if again := m.TakeRoutedTraffic(); len(again) != 0 {
		t.Errorf("a second take = %+v, want nothing", again)
	}
}

// A removal, a rebuild or the panel's shutdown closes the interface, and what it carried since the last take must still reach
// the job that charges it.
func TestRoutedTrafficOutlivesTheInterfaceThatCarriedIt(t *testing.T) {
	for _, c := range []struct {
		name string
		drop func(m *Manager, inst amneziawg.Instance)
	}{
		{"removed", func(m *Manager, inst amneziawg.Instance) { m.Remove(inst.Id) }},
		{"stopped with the panel", func(m *Manager, _ amneziawg.Instance) { m.StopAll() }},
		{"dropped with its inbound", func(m *Manager, _ amneziawg.Instance) { m.Reconcile(nil) }},
		{"rebuilt for a new MTU", func(m *Manager, inst amneziawg.Instance) {
			inst.MTU = 1380
			if err := m.Ensure(Desired{Instance: inst}); err != nil {
				t.Fatalf("Ensure with a new MTU: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := newRoutingManager(t)
			inst := routingInstance(t, 27, "10.211.0", true,
				routingPeer(t, "a@x", "10.211.0.2/32"), routingPeer(t, "b@x", "10.211.0.3/32"))
			if err := m.Ensure(Desired{Instance: inst}); err != nil {
				t.Fatalf("Ensure: %v", err)
			}
			dev, _, _ := m.Lookup(27)
			if !dev.tun.forwardToPeer(ipv4Packet("10.211.0.2", "10.211.0.3", 64, "hello")) {
				t.Fatal("the packet was not taken for the other peer")
			}

			c.drop(m, inst)

			got := map[string]RoutedTraffic{}
			for _, r := range m.TakeRoutedTraffic() {
				got[r.Email] = r
			}
			if a := got["a@x"]; a.Up == 0 {
				t.Errorf("a@x = %+v, want what it sent", a)
			}
			if b := got["b@x"]; b.Down == 0 {
				t.Errorf("b@x = %+v, want what it received", b)
			}
			if again := m.TakeRoutedTraffic(); len(again) != 0 {
				t.Errorf("a second take = %+v, want nothing: what was handed over must not be handed over twice", again)
			}
		})
	}
}

func netipAddr(t *testing.T, s string) netip.Addr {
	t.Helper()
	a, err := netip.ParseAddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return a
}
