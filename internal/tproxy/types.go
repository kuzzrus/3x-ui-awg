// Package tproxy manages the Telegram web-proxy (tproxy) sidecar: one shared
// tproxy-server relay plus one MTProxy engine per tproxy inbound.
package tproxy

// ClientSecret is one client's raw MTProto secret: it derives the
// tproxy-server bridge capability and is also MTProxy's own -S value.
type ClientSecret struct {
	// Name must be unique across every tproxy inbound on the panel -- all
	// inbounds share the one tproxy-server process's profiles.json.
	Name   string
	Secret string
}

// Instance is the desired state of one tproxy inbound. Package-local, mirrors
// internal/mtproto's and internal/naiveproxy's own Instance.
type Instance struct {
	Id      int
	Clients []ClientSecret
	// Tag is the inbound's own DB tag, carried through for both the
	// active-inbound online-status signal (RefreshLocalOnlineClients) and,
	// when RouteThroughXray is set, as the tag the dokodemo-door bridge
	// injectTproxyEgress builds shares with this inbound.
	Tag string
	// RouteThroughXray/RouteXrayPort mirror mtproto's own Instance fields in
	// shape, but not in mechanism: mtproto's mtg sidecar can be told to dial
	// out through a SOCKS5 proxy, so its bridge is that proxy's own loopback
	// address. The real vendored MTProxy engine this package supervises has
	// no such flag at all (confirmed against TelegramMessenger/MTProxy's own
	// source), so RouteXrayPort here is instead the target of an OS-level
	// nftables redirect (firewall.go) that transparently forwards the
	// engine's own outbound connections into a dokodemo-door inbound
	// listening on that port -- the engine itself is never told anything.
	RouteThroughXray bool
	RouteXrayPort    int
}
