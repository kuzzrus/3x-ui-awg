package amneziawgnet

import (
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip/header"
)

// peerRoute lets the embedded server route between its peers by their AllowedIPs, as a kernel interface does; without it
// every packet goes into the stack, to be dialed out of the host, so a peer reaches no other peer.
type peerRoute struct {
	peers *PeerIndex
}

// forwardToPeer hands packet, decrypted from one peer, back to the device for another peer when its destination lies in that
// peer's AllowedIPs. It reports whether it took the packet; false leaves it to the stack.
func (t *stackTun) forwardToPeer(packet []byte) bool {
	route := t.peerRoute.Load()
	if route == nil {
		return false
	}
	src, dst, ok := packetEnds(packet)
	// What is not for one address of one peer stays with the stack: a multicast address belongs to no peer.
	if !ok || dst.IsMulticast() || dst.IsUnspecified() {
		return false
	}
	to, ok := route.peers.LookupRouted(dst)
	if !ok {
		return false
	}
	// A packet for the range of the peer that sent it is dropped, not returned: it is a loop, and only the hop limit would end it.
	from, fromOK := route.peers.Lookup(src)
	if fromOK && from.PublicKey == to.PublicKey {
		return true
	}
	if !decrementHops(packet) {
		return true
	}
	// Xray never sees these packets, so it cannot count them: those that were sent on are counted here, to be charged to the
	// two clients.
	if t.enqueue(buffer.NewViewWithData(slices.Clone(packet))) {
		t.routed.add(from.Email, to.Email, len(packet))
	}
	return true
}

// peerBytes is what one peer has sent to, and received from, other peers since it was last taken.
type peerBytes struct{ up, down atomic.Int64 }

// routedTraffic counts the bytes of the packets that went from one peer to another, by the email of the client that owns the peer.
type routedTraffic struct{ byEmail sync.Map }

func (r *routedTraffic) add(from, to string, n int) {
	if from != "" {
		r.entry(from).up.Add(int64(n))
	}
	if to != "" {
		r.entry(to).down.Add(int64(n))
	}
}

func (r *routedTraffic) entry(email string) *peerBytes {
	if v, ok := r.byEmail.Load(email); ok {
		return v.(*peerBytes)
	}
	v, _ := r.byEmail.LoadOrStore(email, &peerBytes{})
	return v.(*peerBytes)
}

// RoutedTraffic is the bytes a client sent to other clients and received from them, for the traffic limits.
type RoutedTraffic struct {
	Email    string
	Up, Down int64
}

// take returns what was counted since the last call, and counts from nothing again.
func (r *routedTraffic) take() []RoutedTraffic {
	var out []RoutedTraffic
	r.byEmail.Range(func(k, v any) bool {
		b := v.(*peerBytes)
		if up, down := b.up.Swap(0), b.down.Swap(0); up != 0 || down != 0 {
			out = append(out, RoutedTraffic{Email: k.(string), Up: up, Down: down})
		}
		return true
	})
	return out
}

// packetEnds is the source and destination of an IP packet; false for one that is not a whole IPv4 or IPv6 packet.
func packetEnds(packet []byte) (src, dst netip.Addr, ok bool) {
	switch packet[0] >> 4 {
	case 4:
		ip := header.IPv4(packet)
		if ip.IsValid(len(packet)) {
			return addrFromTcpip(ip.SourceAddress()), addrFromTcpip(ip.DestinationAddress()), true
		}
	case 6:
		ip := header.IPv6(packet)
		if ip.IsValid(len(packet)) {
			return addrFromTcpip(ip.SourceAddress()), addrFromTcpip(ip.DestinationAddress()), true
		}
	}
	return netip.Addr{}, netip.Addr{}, false
}

// decrementHops takes one hop off packet, as any router does, and reports whether it had one to spare.
func decrementHops(packet []byte) bool {
	if packet[0]>>4 == 4 {
		ip := header.IPv4(packet)
		if ip.TTL() <= 1 {
			return false
		}
		ip.SetTTL(ip.TTL() - 1)
		ip.SetChecksum(0)
		ip.SetChecksum(^ip.CalculateChecksum())
		return true
	}
	ip := header.IPv6(packet)
	if ip.HopLimit() <= 1 {
		return false
	}
	ip.SetHopLimit(ip.HopLimit() - 1)
	return true
}
