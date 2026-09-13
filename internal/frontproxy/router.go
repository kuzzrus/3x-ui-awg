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
}

// resolveTarget picks the destination for one request path and query. Sub is
// checked before panel; tproxy last, right before the decoy fallback.
func (c Config) resolveTarget(path, rawQuery string) Route {
	if c.SubEnabled && matchesPrefix(path, c.SubPath) {
		return RouteSub
	}
	if matchesPrefix(path, c.PanelBasePath) {
		return RoutePanel
	}
	if c.TproxyTarget != "" && c.matchesTproxyCapability(rawQuery) {
		return RouteTproxy
	}
	return RouteDecoy
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
