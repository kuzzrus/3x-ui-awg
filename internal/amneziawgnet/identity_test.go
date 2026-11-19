package amneziawgnet

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// TestPeerIndexLookupMatchesDeviceAllowedIPs catches Lookup attributing a
// prefix two peers both carry to a peer the device accepts no such source from.
func TestPeerIndexLookupMatchesDeviceAllowedIPs(t *testing.T) {
	serverPriv, serverPub, err := wireguard.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("generate server keypair: %v", err)
	}
	var peers []amneziawg.Peer
	for _, p := range []amneziawg.Peer{
		{Email: "first@example.com", AllowedIPs: []string{"10.202.0.2/32", "fd00:202::2/128", "0.0.0.0/0", "::/0"}},
		{Email: "last@example.com", AllowedIPs: []string{"10.202.0.3/32", "fd00:202::3/128", "0.0.0.0/0", "::/0"}},
	} {
		if _, p.PublicKey, err = wireguard.GenerateWireguardKeypair(); err != nil {
			t.Fatalf("generate peer keypair: %v", err)
		}
		peers = append(peers, p)
	}

	dev, err := NewDevice(amneziawg.Instance{
		Id:         1,
		PrivateKey: serverPriv,
		PublicKey:  serverPub,
		Address:    []string{"10.202.0.1/24"},
		MTU:        1420,
		Peers:      peers,
	}, DeviceOptions{})
	if err != nil {
		t.Fatalf("NewDevice: %v", err)
	}
	defer dev.Close()

	dump, err := dev.IpcGet()
	if err != nil {
		t.Fatalf("IpcGet: %v", err)
	}
	_, states := parseUAPIDump(dump)

	idx := NewPeerIndex(peers)
	for _, tc := range []struct{ addr, prefix string }{
		{"10.99.0.1", "0.0.0.0/0"},
		{"fd00:99::1", "::/0"},
		{"10.202.0.2", "10.202.0.2/32"},
		{"10.202.0.3", "10.202.0.3/32"},
		{"fd00:202::2", "fd00:202::2/128"},
		{"fd00:202::3", "fd00:202::3/128"},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			got, ok := idx.Lookup(netip.MustParseAddr(tc.addr))
			if !ok {
				t.Fatalf("Lookup(%s) found no peer", tc.addr)
			}
			gotHex, err := wireguard.KeyToHex(got.PublicKey)
			if err != nil {
				t.Fatalf("KeyToHex: %v", err)
			}
			if !slices.Contains(states[gotHex].allowedIPs, tc.prefix) {
				t.Errorf("Lookup(%s) = %s, but the device holds %s for another peer", tc.addr, got.Email, tc.prefix)
			}
		})
	}
}

// A default route claims no network: a peer that carries one (a client's own AllowedIPs pasted into its server entry, say) must
// not draw the internet traffic of the other clients to itself once the server routes between them.
func TestPeerIndexLookupRoutedIgnoresDefaultRoutes(t *testing.T) {
	idx := NewPeerIndex([]amneziawg.Peer{
		{PublicKey: "gw", Email: "gw@x", AllowedIPs: []string{"10.9.0.2/32", "0.0.0.0/0", "::/0"}},
		{PublicKey: "halves", Email: "halves@x", AllowedIPs: []string{"10.9.0.3/32", "0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"}},
		{PublicKey: "corp", Email: "corp@x", AllowedIPs: []string{"10.9.0.4/32", "10.0.0.0/8", "fd00::/8"}},
		{PublicKey: "wide", Email: "wide@x", AllowedIPs: []string{"10.9.0.5/32", "12.0.0.0/7"}},
	})
	email := func(p amneziawg.Peer, ok bool) string {
		if !ok {
			return ""
		}
		return p.Email
	}
	for _, tc := range []struct {
		name, addr    string
		plain, routed string // the email that Lookup and LookupRouted find, "" for none
	}{
		{"an address of the internet", "8.8.8.8", "halves@x", ""},
		{"the upper half of the internet", "200.1.1.1", "halves@x", ""},
		{"an IPv6 address of the internet", "2001:db8::1", "halves@x", ""},
		{"the upper half of IPv6", "a000::1", "halves@x", ""},
		{"a peer's own address", "10.9.0.2", "gw@x", "gw@x"},
		{"a network of 8 bits", "10.5.5.5", "corp@x", "corp@x"},
		{"an IPv6 network of 8 bits", "fd00::5", "corp@x", "corp@x"},
		{"a network of 7 bits", "12.1.1.1", "wide@x", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := netip.MustParseAddr(tc.addr)
			if got := email(idx.Lookup(addr)); got != tc.plain {
				t.Errorf("Lookup(%s) = %q, want %q", tc.addr, got, tc.plain)
			}
			if got := email(idx.LookupRouted(addr)); got != tc.routed {
				t.Errorf("LookupRouted(%s) = %q, want %q", tc.addr, got, tc.routed)
			}
		})
	}
}
