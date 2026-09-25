// Package frontproxy is the panel's own reverse proxy: the single
// local listener Xray's REALITY fallback target points at, which routes by
// URL path to the panel, to the subscription server, or to a decoy site.
//
// This replaces what admins otherwise hand-build with an external nginx. The
// REALITY fallback hands over raw, still-encrypted bytes -- Xray cannot
// decrypt them -- so this listener terminates real TLS itself, exactly like
// that nginx would.
package frontproxy

import (
	"crypto/hmac"
	"net/url"
	"strings"
)

// Route names where a reverse-proxy request should be sent.
type Route int

const (
	// RouteDecoy is the fallback: anything not matching a secret path.
	RouteDecoy Route = iota
	// RoutePanel is the admin panel, reached under its own base path.
	RoutePanel
	// RouteSub is the subscription server, reached under its own path.
	RouteSub
	// RouteTproxy is the Telegram web-proxy relay, selected by an
	// authenticated ?bridge= capability rather than a path prefix.
	RouteTproxy
)

// Config is the routing half of the reverse proxy, resolved from settings.
// Ports are loopback targets on this same host.
type Config struct {
	PanelBasePath string
	PanelPort     int
	SubPath       string
	SubPort       int
	SubEnabled    bool
	// UpstreamTLS is set when the panel and subscription listeners serve TLS
	// themselves, which they do whenever certificate files are configured.
	UpstreamTLS bool
	// TproxyCapabilities are every active tproxy client's precomputed
	// DeriveCapability value; empty disables the route entirely.
	TproxyCapabilities []string
	// TproxyTarget is the shared tproxy-server relay's loopback address.
	TproxyTarget string
	// PathTargets lets an admin-chosen XHTTP/WS inbound be reached under a
	// path on this same public domain, instead of needing a hand-built
	// external nginx location block (the CDN-fronting use case). Order
	// matters: first prefix match wins, same convention as everything else
	// in this file. Always loopback (127.0.0.1) -- like Panel/Sub, this is
	// never a target off-box.
	PathTargets []PathTarget
}

// PathTarget is one row of the admin-configured path routing list --
// resolved (ChildId -> loopback port) before it ever reaches this package,
// same division of responsibility as TproxyCapabilities/TproxyTarget above.
type PathTarget struct {
	Path string
	Port int
}

// tproxyAPIPrefix is tproxy-server's own bridge session API -- PROTOCOL.md's
// /api/v1/session, /api/v1/up, /api/v1/down, /api/v1/ws, fixed by the
// upstream wire protocol itself, not an admin-chosen secret like
// PanelBasePath/SubPath. Only the bootstrap GET / carries ?bridge=; every
// request after it authenticates with an "Authorization: Bearer <token>"
// header instead, which the query-only matchesTproxyCapability check below
// can never see. Safe to route unconditionally by path once tproxy is
// enabled: tproxy-server itself rejects a missing or wrong bearer token by
// falling back to its own public_dir/public_upstream camouflage (see
// PROTOCOL.md's "Requests with no authentic secret follow the ordinary
// public handler"), the same trust model RoutePanel/RouteSub already use.
const tproxyAPIPrefix = "api/v1"

// resolveTarget picks the destination for one request path and query. Sub is
// checked before panel; tproxy last, right before the decoy fallback.
func (c Config) resolveTarget(path, rawQuery string) Route {
	if c.SubEnabled && matchesPrefix(path, c.SubPath) {
		return RouteSub
	}
	if matchesPrefix(path, c.PanelBasePath) {
		return RoutePanel
	}
	if c.TproxyTarget != "" && (c.matchesTproxyCapability(rawQuery) || matchesPrefix(path, tproxyAPIPrefix)) {
		return RouteTproxy
	}
	return RouteDecoy
}

// resolvePathTarget checks the admin-configured path list -- called by
// newHandler only once resolveTarget has already fallen through to
// RouteDecoy, so a path route can never shadow Panel/Sub/Tproxy's own
// (secret or protocol-mandated) paths, only the decoy that would otherwise
// answer everything else.
func (c Config) resolvePathTarget(path string) (int, bool) {
	for _, t := range c.PathTargets {
		if matchesPrefix(path, t.Path) {
			return t.Port, true
		}
	}
	return 0, false
}

// matchesTproxyCapability checks every candidate, not just up to the first
// match, so response time cannot reveal how close a wrong guess came.
func (c Config) matchesTproxyCapability(rawQuery string) bool {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return false
	}
	bridge := values.Get("bridge")
	if bridge == "" {
		return false
	}
	found := false
	for _, capability := range c.TproxyCapabilities {
		if hmac.Equal([]byte(bridge), []byte(capability)) {
			found = true
		}
	}
	return found
}

// matchesPrefix reports whether path is at or under base. A root or empty
// base never matches: it would swallow every request and hide the decoy.
func matchesPrefix(path, base string) bool {
	trimmed := strings.Trim(base, "/")
	if trimmed == "" {
		return false
	}
	exact := "/" + trimmed
	return path == exact || strings.HasPrefix(path, exact+"/")
}
