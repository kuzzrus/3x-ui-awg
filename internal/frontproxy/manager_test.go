package frontproxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strconv"
	"strings"
	"testing"
)

// upstreamOn starts a loopback server that echoes a marker, and returns its
// port so the reverse proxy (which always dials 127.0.0.1) can reach it.
func upstreamOn(t *testing.T, marker string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(marker + " " + r.URL.Path +
			" proto=" + r.Header.Get("X-Forwarded-Proto") + " host=" + r.Host))
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestHandlerDispatchesToEachUpstream(t *testing.T) {
	panelPort := upstreamOn(t, "PANEL")
	subPort := upstreamOn(t, "SUB")

	h := newHandler(Config{
		PanelBasePath: "/secretpanel/",
		PanelPort:     panelPort,
		SubPath:       "/secretsub/",
		SubPort:       subPort,
		SubEnabled:    true,
	}, DecoyConfig{Mode: DecoyTemplate, Template: "parked"})

	cases := []struct {
		path string
		want string
	}{
		{"/secretpanel/panel/inbounds", "PANEL"},
		{"/secretsub/token123", "SUB"},
		{"/", "Здесь"},
		{"/wp-admin", "Здесь"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s -> %q, want it to contain %q", tc.path, rec.Body.String(), tc.want)
		}
	}
}

// The hop to the panel is plaintext loopback while the real client arrived
// over TLS; without this header the panel would build http:// links.
func TestHandlerMarksForwardedProtoHTTPS(t *testing.T) {
	panelPort := upstreamOn(t, "PANEL")
	h := newHandler(Config{PanelBasePath: "/p/", PanelPort: panelPort}, DecoyConfig{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/x", nil))
	if !strings.Contains(rec.Body.String(), "proto=https") {
		t.Errorf("X-Forwarded-Proto not set to https, got %q", rec.Body.String())
	}
}

// The panel must see the hostname the client actually asked for, not the
// loopback target, or it builds its links and cookies against 127.0.0.1.
func TestHandlerPreservesClientHost(t *testing.T) {
	panelPort := upstreamOn(t, "PANEL")
	h := newHandler(Config{PanelBasePath: "/p/", PanelPort: panelPort}, DecoyConfig{})

	req := httptest.NewRequest(http.MethodGet, "/p/x", nil)
	req.Host = "panel.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "host=panel.example.com") {
		t.Errorf("client Host not forwarded, got %q", rec.Body.String())
	}
}

// tlsUpstreamOn stands in for a panel that has certificate files configured
// and therefore serves HTTPS on its own port.
func tlsUpstreamOn(t *testing.T, marker string) int {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(marker + " " + r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	_, portStr, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "https://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// A panel holding its own certificates answers a plaintext hop with a 307 back
// to the URL the client already asked for, which browsers follow until they
// give up. The hop has to speak TLS to it instead.
func TestHandlerSpeaksTLSToATLSUpstream(t *testing.T) {
	h := newHandler(Config{
		PanelBasePath: "/p/",
		PanelPort:     tlsUpstreamOn(t, "PANEL"),
		UpstreamTLS:   true,
	}, DecoyConfig{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/panel/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "PANEL") {
		t.Errorf("body = %q, want it to reach the TLS upstream", rec.Body.String())
	}
}

// The mirror case: a plaintext panel must still be dialled in plaintext.
func TestHandlerSpeaksPlaintextToAPlainUpstream(t *testing.T) {
	h := newHandler(Config{PanelBasePath: "/p/", PanelPort: upstreamOn(t, "PANEL")}, DecoyConfig{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/panel/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
}

// A connection pooled across the panel's own slow requests has been observed
// ending in a reset the panel never gets a chance to log (see manager.go's
// newLoopbackProxy). Asserting on the emergent behavior -- two requests
// through httptest.NewRecorder, expecting either a distinct r.RemoteAddr or
// a distinct r.Close per request -- does not reliably tell the pooled and
// unpooled cases apart: httputil.ReverseProxy's handling of a
// ResponseRecorder that never had a real net.Conn behind it does not
// reproduce the pooling behavior a real front-end connection sees. This
// asserts the one thing that is actually deterministic: the loopback
// transport's own configuration.
func TestLoopbackProxyTransportDisablesKeepAlives(t *testing.T) {
	for _, useTLS := range []bool{false, true} {
		proxy, ok := newLoopbackProxy(1, useTLS).(*httputil.ReverseProxy)
		if !ok {
			t.Fatalf("useTLS=%v: newLoopbackProxy did not return a *httputil.ReverseProxy", useTLS)
		}
		transport, ok := proxy.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("useTLS=%v: Transport is a %T, want *http.Transport", useTLS, proxy.Transport)
		}
		if !transport.DisableKeepAlives {
			t.Errorf("useTLS=%v: DisableKeepAlives = false, want true", useTLS)
		}
	}
}

// A dead upstream must produce a plain 502, never a panic that would take
// the whole reverse proxy (and with it the decoy) down.
func TestHandlerSurvivesDeadUpstream(t *testing.T) {
	h := newHandler(Config{PanelBasePath: "/p/", PanelPort: 1}, DecoyConfig{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/p/x", nil))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 from an unreachable upstream", rec.Code)
	}
}

// A valid bridge must reach the relay with the client's original Host intact
// -- tproxy-server itself 404s any Host but its own public_hostname.
func TestHandlerRoutesValidBridgeToTproxyRelay(t *testing.T) {
	tproxyPort := upstreamOn(t, "TPROXY")
	h := newHandler(Config{
		PanelBasePath:      "/p/",
		PanelPort:          1,
		TproxyTarget:       "127.0.0.1:" + strconv.Itoa(tproxyPort),
		TproxyCapabilities: []string{"real-capability"},
	}, DecoyConfig{Mode: DecoyTemplate, Template: "parked"})

	req := httptest.NewRequest(http.MethodGet, "/?bridge=real-capability", nil)
	req.Host = "proxy.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if !strings.Contains(body, "TPROXY") {
		t.Fatalf("did not reach the tproxy relay, got %q", body)
	}
	if !strings.Contains(body, "host=proxy.example.com") {
		t.Errorf("relay saw the wrong Host, got %q", body)
	}
}

// A wrong or missing bridge value must fall through to the decoy, not the
// relay -- the entire point of checking it last.
func TestHandlerFallsThroughToDecoyOnInvalidBridge(t *testing.T) {
	tproxyPort := upstreamOn(t, "TPROXY")
	h := newHandler(Config{
		TproxyTarget:       "127.0.0.1:" + strconv.Itoa(tproxyPort),
		TproxyCapabilities: []string{"real-capability"},
	}, DecoyConfig{Mode: DecoyTemplate, Template: "parked"})

	for _, path := range []string{"/?bridge=wrong", "/"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if strings.Contains(rec.Body.String(), "TPROXY") {
			t.Errorf("%s reached the tproxy relay, want the decoy", path)
		}
	}
}

// Starting with a root base path must be refused: the door could not tell
// the panel apart from the decoy, so every request would hit the panel.
func TestStartRejectsRootBasePath(t *testing.T) {
	m := &Manager{}
	err := m.Start(Options{
		Port:    8443,
		Routing: Config{PanelBasePath: "/", PanelPort: 2053},
		TLS:     TLSSettings{Mode: CertManual, CertFile: "x", KeyFile: "y"},
	})
	if err == nil {
		t.Fatal("Start succeeded with a root base path, want refusal")
	}
	if m.IsRunning() {
		t.Error("manager reports running after a refused Start")
	}
}

func TestStartRejectsInvalidPort(t *testing.T) {
	for _, port := range []int{0, -1, 70000} {
		m := &Manager{}
		if err := m.Start(Options{Port: port, Routing: Config{PanelBasePath: "/p/", PanelPort: 2053}}); err == nil {
			t.Errorf("Start succeeded with port %d, want refusal", port)
		}
	}
}

// Stop on a manager that never started is a no-op, so panel shutdown can
// call it unconditionally.
func TestStopWhenNotRunningIsNoOp(t *testing.T) {
	m := &Manager{}
	if err := m.Stop(); err != nil {
		t.Errorf("Stop on an idle manager returned %v, want nil", err)
	}
	m.StopAll()
}

// Changing the decoy must take effect on the running listener. Rebuilding it
// through Stop/Start would drop connections and redo the TLS setup for what
// is only a change of handler.
func TestReloadSwapsTheDecoyWithoutRestarting(t *testing.T) {
	m := &Manager{}
	m.store(newHandler(Config{PanelBasePath: "/p/", PanelPort: 1},
		DecoyConfig{Mode: DecoyTemplate, Template: "maintenance"}))

	before := httptest.NewRecorder()
	m.dispatch(before, httptest.NewRequest(http.MethodGet, "/", nil))
	firstTag := before.Header().Get("ETag")

	m.store(newHandler(Config{PanelBasePath: "/p/", PanelPort: 1},
		DecoyConfig{Mode: DecoyTemplate, Template: "tetris"}))

	after := httptest.NewRecorder()
	m.dispatch(after, httptest.NewRequest(http.MethodGet, "/", nil))
	if after.Header().Get("ETag") == firstTag {
		t.Error("the decoy did not change after the handler was swapped")
	}
	if !strings.Contains(after.Body.String(), "<html") {
		t.Errorf("swapped decoy served no page: %q", after.Body.String())
	}
}

// A path-routed target is reached under its configured path, alongside
// Panel/Sub/Tproxy and the decoy -- same dispatch shape as
// TestHandlerDispatchesToEachUpstream, one more branch.
func TestHandlerDispatchesToPathTarget(t *testing.T) {
	// tlsUpstreamOn, not upstreamOn: path targets always dial TLS (see
	// TestHandlerPathTargetAlwaysUsesTLS below for why), so a plaintext
	// backend here would fail the handshake, not just be the wrong test.
	cdnPort := tlsUpstreamOn(t, "CDN")
	h := newHandler(Config{
		PanelBasePath: "/secretpanel/",
		PanelPort:     1,
		PathTargets:   []PathTarget{{Path: "/xh-cdn-K7m4Qp9s", Port: cdnPort}},
	}, DecoyConfig{Mode: DecoyTemplate, Template: "parked"})

	cases := []struct {
		path string
		want string
	}{
		{"/xh-cdn-K7m4Qp9s/anything", "CDN"},
		{"/other-path", "Здесь"},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s -> %q, want it to contain %q", tc.path, rec.Body.String(), tc.want)
		}
	}
}

// The core safety guarantee this feature depends on: resolveTarget's own
// Panel/Sub/Tproxy routes are checked before resolvePathTarget is ever
// consulted (see newHandler's switch), so an admin-configured path route can
// never shadow one of them -- even one deliberately set to collide, which
// FrontProxyPathService.SetAll's own validation should reject before this
// ever happens in production, but the dispatcher itself must not rely on
// that validation always having run.
func TestHandlerPanelWinsOverCollidingPathTarget(t *testing.T) {
	panelPort := upstreamOn(t, "PANEL")
	cdnPort := upstreamOn(t, "CDN")
	h := newHandler(Config{
		PanelBasePath: "/shared/",
		PanelPort:     panelPort,
		PathTargets:   []PathTarget{{Path: "/shared", Port: cdnPort}},
	}, DecoyConfig{Mode: DecoyTemplate, Template: "parked"})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/shared/inbounds", nil))
	if !strings.Contains(rec.Body.String(), "PANEL") {
		t.Errorf("got %q, want the panel to win over a colliding path target", rec.Body.String())
	}
}

// XHTTP's stream-one/stream-up modes hang indefinitely over a plaintext
// loopback hop (xray-core's own splithttp dialer forces HTTP/1.1 whenever
// TLS is absent) -- confirmed live, 2026-09-25 PoC on awg-test-vps. Every
// path-routed target must therefore dial its loopback backend over TLS,
// unconditionally, regardless of Routing.UpstreamTLS (which only describes
// the panel/sub listeners, an unrelated setting).
func TestHandlerPathTargetAlwaysUsesTLS(t *testing.T) {
	cdnPort := tlsUpstreamOn(t, "CDN")
	h := newHandler(Config{
		PanelBasePath: "/p/",
		PanelPort:     1,
		UpstreamTLS:   false,
		PathTargets:   []PathTarget{{Path: "/cdn", Port: cdnPort}},
	}, DecoyConfig{})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/cdn/x", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "CDN") {
		t.Errorf("status=%d body=%q, want 200 reaching the TLS-only upstream even though UpstreamTLS=false", rec.Code, rec.Body.String())
	}
}

// Reload on a stopped manager must not resurrect it, or a settings save would
// silently start a proxy the admin had turned off.
func TestReloadOnStoppedManagerStaysStopped(t *testing.T) {
	m := &Manager{}
	m.Reload(Options{Port: 8443, Routing: Config{PanelBasePath: "/p/", PanelPort: 2053}})
	if m.IsRunning() {
		t.Error("Reload started a manager that was not running")
	}
}
