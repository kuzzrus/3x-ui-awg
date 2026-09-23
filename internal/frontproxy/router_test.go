package frontproxy

import "testing"

func testConfig() Config {
	return Config{
		PanelBasePath: "/nAMUGqBqnQ6crf3zvE/",
		PanelPort:     52973,
		SubPath:       "/pojht0vsfvseghbdnr/",
		SubPort:       35985,
		SubEnabled:    true,
	}
}

func TestResolveTargetRoutesSecretPaths(t *testing.T) {
	c := testConfig()
	cases := []struct {
		path string
		want Route
	}{
		{"/nAMUGqBqnQ6crf3zvE/", RoutePanel},
		{"/nAMUGqBqnQ6crf3zvE", RoutePanel},
		{"/nAMUGqBqnQ6crf3zvE/panel/inbounds", RoutePanel},
		{"/nAMUGqBqnQ6crf3zvE/ws", RoutePanel},
		{"/pojht0vsfvseghbdnr/", RouteSub},
		{"/pojht0vsfvseghbdnr/abc123", RouteSub},
		{"/", RouteDecoy},
		{"/index.html", RouteDecoy},
		{"/wp-login.php", RouteDecoy},
	}
	for _, tc := range cases {
		if got := c.resolveTarget(tc.path, ""); got != tc.want {
			t.Errorf("resolveTarget(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// A path that merely starts with the same letters is a different path, and
// must not leak the panel to an outsider probing near-miss prefixes.
func TestResolveTargetRejectsPrefixLookalikes(t *testing.T) {
	c := testConfig()
	for _, path := range []string{"/nAMUGqBqnQ6crf3zvEX", "/nAMUGqBqnQ6crf3zvE-admin", "/pojht0vsfvseghbdnrX"} {
		if got := c.resolveTarget(path, ""); got != RouteDecoy {
			t.Errorf("resolveTarget(%q) = %v, want RouteDecoy", path, got)
		}
	}
}

// Go leaves ".." in r.URL.Path, so dispatch sees traversing paths verbatim.
// They must never resolve to the panel or the subscription without the real
// prefix actually leading the path.
func TestResolveTargetRejectsTraversalIntoSecrets(t *testing.T) {
	c := testConfig()
	for _, path := range []string{
		"/x/../nAMUGqBqnQ6crf3zvE/panel",
		"/../nAMUGqBqnQ6crf3zvE/",
		"/x/../pojht0vsfvseghbdnr/abc",
		"/..%2fnAMUGqBqnQ6crf3zvE/",
	} {
		if got := c.resolveTarget(path, ""); got != RouteDecoy {
			t.Errorf("resolveTarget(%q) = %v, want RouteDecoy", path, got)
		}
	}
}

// With the subscription server switched off its path is not special, so it
// falls through to the decoy rather than proxying to a dead port.
func TestResolveTargetIgnoresSubPathWhenDisabled(t *testing.T) {
	c := testConfig()
	c.SubEnabled = false
	if got := c.resolveTarget("/pojht0vsfvseghbdnr/abc", ""); got != RouteDecoy {
		t.Errorf("got %v, want RouteDecoy when sub is disabled", got)
	}
}

// A root base path cannot be distinguished from the decoy, so it must never
// match -- otherwise every request would be swallowed by the panel route.
func TestResolveTargetRootBasePathNeverMatches(t *testing.T) {
	for _, base := range []string{"/", "", "//"} {
		c := Config{PanelBasePath: base, PanelPort: 2053}
		for _, path := range []string{"/", "/anything", "/panel/"} {
			if got := c.resolveTarget(path, ""); got != RouteDecoy {
				t.Errorf("base %q: resolveTarget(%q) = %v, want RouteDecoy", base, path, got)
			}
		}
	}
}

// The stored setting may or may not carry surrounding slashes; both spellings
// have to resolve identically or the door breaks on a cosmetic settings edit.
func TestResolveTargetToleratesSlashSpelling(t *testing.T) {
	for _, base := range []string{"/secret/", "secret", "/secret", "secret/"} {
		c := Config{PanelBasePath: base, PanelPort: 2053}
		if got := c.resolveTarget("/secret/panel", ""); got != RoutePanel {
			t.Errorf("base %q: got %v, want RoutePanel", base, got)
		}
		if got := c.resolveTarget("/other", ""); got != RouteDecoy {
			t.Errorf("base %q: got %v, want RouteDecoy", base, got)
		}
	}
}

func testConfigWithTproxy() Config {
	c := testConfig()
	c.TproxyTarget = "127.0.0.1:19999"
	c.TproxyCapabilities = []string{"cap-alice", "cap-bob"}
	return c
}

// A known capability reaches the relay regardless of path -- tproxy-server
// itself is the authority on which paths its own protocol accepts.
func TestResolveTargetRoutesValidBridgeCapability(t *testing.T) {
	c := testConfigWithTproxy()
	for _, path := range []string{"/", "/anything"} {
		if got := c.resolveTarget(path, "bridge=cap-bob"); got != RouteTproxy {
			t.Errorf("resolveTarget(%q, bridge=cap-bob) = %v, want RouteTproxy", path, got)
		}
	}
}

// An unrecognized, missing, or malformed bridge value must read exactly like
// an ordinary request to a domain with no tproxy configured at all.
func TestResolveTargetRejectsInvalidBridgeCapability(t *testing.T) {
	c := testConfigWithTproxy()
	cases := []string{
		"",
		"bridge=",
		"bridge=wrong-capability",
		"bridge=wrong&bridge=also-wrong",
		"%zz", // unparseable query
	}
	for _, q := range cases {
		if got := c.resolveTarget("/", q); got != RouteDecoy {
			t.Errorf("resolveTarget(\"/\", %q) = %v, want RouteDecoy", q, got)
		}
	}
}

// url.Values.Get returns the first value for a repeated key; a genuine
// capability in that position must still be recognized.
func TestResolveTargetUsesFirstBridgeValueOnDuplicateParams(t *testing.T) {
	c := testConfigWithTproxy()
	if got := c.resolveTarget("/", "bridge=cap-bob&bridge=wrong"); got != RouteTproxy {
		t.Errorf("resolveTarget with a valid first bridge value = %v, want RouteTproxy", got)
	}
}

// Known panel/sub paths win over a coincidentally-present bridge query --
// checked first, so a client link is never mistaken for a bridge request.
func TestResolveTargetPanelSubTakePriorityOverBridge(t *testing.T) {
	c := testConfigWithTproxy()
	if got := c.resolveTarget("/nAMUGqBqnQ6crf3zvE/panel", "bridge=cap-bob"); got != RoutePanel {
		t.Errorf("got %v, want RoutePanel even with a valid bridge query present", got)
	}
}

// With no tproxy client configured, TproxyTarget is empty and the route must
// never activate, even if a caller somehow supplied a matching capability.
func TestResolveTargetTproxyDisabledWhenNoTarget(t *testing.T) {
	c := testConfig()
	c.TproxyCapabilities = []string{"cap-alice"}
	if got := c.resolveTarget("/", "bridge=cap-alice"); got != RouteDecoy {
		t.Errorf("got %v, want RouteDecoy when TproxyTarget is empty", got)
	}
}

// Regression for a real production bug: only the bootstrap GET / carries a
// ?bridge= query (PROTOCOL.md). Every request after it -- session creation,
// uplink, downlink poll, the websocket upgrade -- authenticates with an
// "Authorization: Bearer <token>" header instead, which resolveTarget's
// query-only bridge check can never see. Unfixed, every one of these fell
// through to RouteDecoy: a live capture showed the bootstrap page load fine
// (its GET carries ?bridge=) while the browser's own immediately-following
// POST /api/v1/session, carrying a fresh, genuinely valid bootstrap token,
// got the decoy's 401 instead of tproxy-server's real session response --
// verified by replaying the identical request straight at tproxy-server,
// bypassing this router, and getting 200. No capability was ever verified
// invalid; the request just never reached the relay that could check it.
func TestResolveTargetRoutesTproxyAPIPathsWithoutBridgeQuery(t *testing.T) {
	c := testConfigWithTproxy()
	for _, path := range []string{
		"/api/v1/session",
		"/api/v1/up",
		"/api/v1/down",
		"/api/v1/ws",
		"/api/v1/",
		"/api/v1",
	} {
		if got := c.resolveTarget(path, ""); got != RouteTproxy {
			t.Errorf("resolveTarget(%q, \"\") = %v, want RouteTproxy", path, got)
		}
	}
}

// Same paths must not activate when tproxy itself is disabled, matching
// TestResolveTargetTproxyDisabledWhenNoTarget's bridge-query counterpart.
func TestResolveTargetTproxyAPIPathsDisabledWhenNoTarget(t *testing.T) {
	c := testConfig()
	if got := c.resolveTarget("/api/v1/session", ""); got != RouteDecoy {
		t.Errorf("got %v, want RouteDecoy when TproxyTarget is empty", got)
	}
}

// A path that merely starts with the same letters must not match, mirroring
// TestResolveTargetRejectsPrefixLookalikes for the other secret-ish routes.
func TestResolveTargetRejectsTproxyAPIPrefixLookalikes(t *testing.T) {
	c := testConfigWithTproxy()
	for _, path := range []string{"/api/v1x", "/api/v10/session", "/api/v2/session", "/api-v1/session"} {
		if got := c.resolveTarget(path, ""); got != RouteDecoy {
			t.Errorf("resolveTarget(%q, \"\") = %v, want RouteDecoy", path, got)
		}
	}
}

// Known panel/sub paths still win even if one were ever configured to
// overlap /api/v1 -- same priority guarantee as the bridge-query case.
func TestResolveTargetPanelSubTakePriorityOverTproxyAPIPath(t *testing.T) {
	c := testConfigWithTproxy()
	c.PanelBasePath = "/api/v1/"
	if got := c.resolveTarget("/api/v1/session", ""); got != RoutePanel {
		t.Errorf("got %v, want RoutePanel when PanelBasePath itself is /api/v1/", got)
	}
}
