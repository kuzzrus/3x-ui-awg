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
	// Tag is the inbound's own DB tag, carried through only for the
	// active-inbound online-status signal (RefreshLocalOnlineClients) --
	// unlike mtproto's Instance.Tag, it is never used for Xray-routed
	// traffic bucketing, since tproxy has no such bridge.
	Tag string
}
