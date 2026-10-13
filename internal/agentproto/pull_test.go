package agentproto

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newTestSecret(t *testing.T) string {
	t.Helper()
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	return secret
}

func TestPullBundleRoundTrip(t *testing.T) {
	b, err := NewPullBundle("https://panel.example.com:2096/secret/", 7, newTestSecret(t), "")
	if err != nil {
		t.Fatalf("NewPullBundle: %v", err)
	}
	token, err := b.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := ParseBundle(token)
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	if *got != *b {
		t.Fatalf("round trip changed the bundle:\n got %+v\nwant %+v", got, b)
	}
	if !got.Pull() {
		t.Fatal("a pull bundle does not say it is one")
	}
	if got.Address != "" || got.Port != 0 || got.CertPEM != "" || got.KeyPEM != "" {
		t.Fatalf("a pull bundle carries what only a push one needs: %+v", got)
	}
	if raw := decodeToken(t, token); strings.Contains(raw, `"cert"`) || strings.Contains(raw, `"key"`) {
		t.Fatalf("the token of a pull bundle holds a certificate or a key: %s", raw)
	}
}

// decodeToken is the JSON inside an encoded bundle.
func decodeToken(t *testing.T, token string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, BundlePrefix))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestPushBundleIsNotAPullBundle(t *testing.T) {
	b, _ := newTestBundle(t)
	if b.Pull() {
		t.Fatal("a push bundle says it is a pull one")
	}
	token, err := b.Encode()
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeToken(t, token)
	for _, field := range []string{`"master"`, `"node"`, `"pin"`} {
		if strings.Contains(raw, field) {
			t.Fatalf("a push bundle carries %s: %s", field, raw)
		}
	}
}

func TestPullBundleRefusesWhatCannotBeDialled(t *testing.T) {
	good := "https://panel.example.com/secret/"
	for name, tc := range map[string]struct {
		master string
		node   int
		pin    string
	}{
		"no master":          {"", 1, ""},
		"plain http":         {"http://panel.example.com/", 1, ""},
		"a user":             {"https://admin:pw@panel.example.com/", 1, ""},
		"a query":            {"https://panel.example.com/?a=b", 1, ""},
		"a fragment":         {"https://panel.example.com/#a", 1, ""},
		"a bad host":         {"https://pa nel.example.com/", 1, ""},
		"a port too big":     {"https://panel.example.com:70000/", 1, ""},
		"a port that is not": {"https://panel.example.com:abc/", 1, ""},
		"no node":            {good, 0, ""},
		"a negative node":    {good, -3, ""},
		"a short pin":        {good, 1, "abcd"},
		"a pin that is not":  {good, 1, strings.Repeat("zz", 32)},
	} {
		if _, err := NewPullBundle(tc.master, tc.node, newTestSecret(t), tc.pin); err == nil {
			t.Errorf("%s: a pull bundle was made for %+v", name, tc)
		}
	}
	if _, err := NewPullBundle(good, 1, "not a secret", ""); err == nil {
		t.Error("a pull bundle was made with a secret that is not one")
	}
}

func TestPullBundleAcceptsAPinAsABrowserShowsIt(t *testing.T) {
	hexPin := strings.Repeat("ab", 32)
	colons := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	for _, pin := range []string{hexPin, colons} {
		if _, err := NewPullBundle("https://panel.example.com/", 1, newTestSecret(t), pin); err != nil {
			t.Errorf("pin %q: %v", pin, err)
		}
	}
}

func TestConnectURL(t *testing.T) {
	for master, want := range map[string]string{
		"https://panel.example.com":                  "wss://panel.example.com/agent/connect/5",
		"https://panel.example.com/":                 "wss://panel.example.com/agent/connect/5",
		"https://panel.example.com:2096/secret/":     "wss://panel.example.com:2096/secret/agent/connect/5",
		"  https://panel.example.com/a/b  ":          "wss://panel.example.com/a/b/agent/connect/5",
		"https://[2001:db8::1]:443/x":                "wss://[2001:db8::1]:443/x/agent/connect/5",
		"https://203.0.113.9:8443/base/with-dash_ok": "wss://203.0.113.9:8443/base/with-dash_ok/agent/connect/5",
	} {
		got, err := ConnectURL(master, 5)
		if err != nil || got != want {
			t.Errorf("ConnectURL(%q) = %q, %v; want %q", master, got, err, want)
		}
	}
}

// wsEcho serves a byte stream that sends back whatever it is sent, and reports the connection it served.
func wsEcho(t *testing.T, onServed func(*Conn)) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{Subprotocols: []string{ConnectProtocol}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(ws, nil)
		if onServed != nil {
			onServed(conn)
		}
		_, _ = io.Copy(conn, conn)
		conn.Close()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func dialConn(t *testing.T, srv *httptest.Server, onClose func()) *Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	dialer := websocket.Dialer{Subprotocols: []string{ConnectProtocol}}
	ws, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	conn := NewConn(ws, onClose)
	t.Cleanup(func() { conn.Close() })
	return conn
}

// A write of several megabytes has to reach the other end as messages it accepts: it limits the size of one.
func TestConnSplitsWhatIsWrittenIntoMessagesThePeerAccepts(t *testing.T) {
	conn := dialConn(t, wsEcho(t, nil), nil)
	payload := make([]byte, 3<<20)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 1)
	go func() {
		_, err := conn.Write(payload)
		errs <- err
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("write: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("what came back is not what was written")
	}
}

func TestConnJoinsMessagesIntoOneStream(t *testing.T) {
	conn := dialConn(t, wsEcho(t, nil), nil)
	for _, part := range []string{"one ", "", "two ", "three"} {
		if _, err := conn.Write([]byte(part)); err != nil {
			t.Fatal(err)
		}
	}
	got := make([]byte, len("one two three"))
	if _, err := io.ReadFull(conn, got); err != nil || string(got) != "one two three" {
		t.Fatalf("read %q, %v", got, err)
	}
	// A read with a buffer smaller than the message takes it in parts, in order.
	if _, err := conn.Write([]byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	small := make([]byte, 3)
	var all []byte
	for len(all) < 10 {
		n, err := conn.Read(small)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, small[:n]...)
	}
	if string(all) != "0123456789" {
		t.Fatalf("read %q", all)
	}
}

func TestConnRunsOnCloseOnceFromEitherEnd(t *testing.T) {
	var served, dialled atomic.Int32
	srv := wsEcho(t, func(c *Conn) { c.onClose = func() { served.Add(1) } })
	conn := dialConn(t, srv, func() { dialled.Add(1) })
	conn.Close()
	conn.Close()
	if n := dialled.Load(); n != 1 {
		t.Fatalf("the closing end ran onClose %d times, want 1", n)
	}
	deadline := time.Now().Add(3 * time.Second)
	for served.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := served.Load(); n != 1 {
		t.Fatalf("the other end ran onClose %d times, want 1", n)
	}
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("a closed connection read")
	}
}

// A peer that goes silent must not hold a connection for ever: it is closed once nothing has come for idle.
func TestKeepAliveClosesAConnectionWhenThePeerGoesSilent(t *testing.T) {
	upgrader := websocket.Upgrader{}
	quiet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Nothing is read here, so no ping is answered with a pong.
		time.Sleep(2 * time.Second)
		ws.Close()
	}))
	t.Cleanup(quiet.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(quiet.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	var closed atomic.Int32
	conn := NewConn(ws, func() { closed.Add(1) })
	t.Cleanup(func() { conn.Close() })
	conn.KeepAlive(20*time.Millisecond, 200*time.Millisecond)

	result := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("the read failed with %v, want the connection to have been closed", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a read from a silent peer is still waiting")
	}
	deadline := time.Now().Add(3 * time.Second)
	for closed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := closed.Load(); n != 1 {
		t.Fatalf("onClose ran %d times, want 1", n)
	}
}

// muteProxy relays one connection to target, and stops passing on what target sends back once it is muted: the path
// is dead to the one that dialled it, which sees no close.
func muteProxy(t *testing.T, target string) (addr string, mute func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var muted atomic.Bool
	go func() {
		client, err := ln.Accept()
		if err != nil {
			return
		}
		defer client.Close()
		upstream, err := net.Dial("tcp", target)
		if err != nil {
			return
		}
		defer upstream.Close()
		go func() { _, _ = io.Copy(upstream, client) }()
		buf := make([]byte, 32<<10)
		for {
			n, err := upstream.Read(buf)
			if n > 0 && !muted.Load() {
				if _, werr := client.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return ln.Addr().String(), func() { muted.Store(true) }
}

// A node is polled all the time, so the connection of its agent is never idle until the path dies: what ends it then is
// the silence, however recent the last request was.
func TestKeepAliveClosesABusyConnectionOnceThePathGoesDead(t *testing.T) {
	upgrader := websocket.Upgrader{Subprotocols: []string{ConnectProtocol}}
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn := NewConn(ws, nil)
		cc, err := NewClientConn(context.Background(), conn)
		if err != nil {
			return
		}
		for conn.heard.Load() != 0 {
			req, _ := http.NewRequest(http.MethodGet, "http://agent.invalid/v1/status", nil)
			resp, err := cc.RoundTrip(req)
			if err != nil {
				return
			}
			_ = resp.Body.Close()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(master.Close)
	addr, mute := muteProxy(t, strings.TrimPrefix(master.URL, "http://"))
	ws, _, err := websocket.DefaultDialer.Dial("ws://"+addr, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := NewConn(ws, nil)
	t.Cleanup(func() { conn.Close() })
	conn.KeepAlive(20*time.Millisecond, 300*time.Millisecond)

	var served atomic.Int32
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		ServeConn(context.Background(), conn, &http.Server{ReadTimeout: time.Minute, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			served.Add(1)
		})})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for served.Load() < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("the master got only %d requests answered", served.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-ended:
		t.Fatal("the connection ended while the master was answering")
	default:
	}

	mute()
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("a connection that the master no longer answers on was still held after 5 seconds")
	}
}

// Data that keeps arriving shows the peer is there even when its answers to the pings are slow to come, as they are
// behind a large transfer on a slow link.
func TestKeepAliveCountsWhatIsReadAsHearingFromThePeer(t *testing.T) {
	upgrader := websocket.Upgrader{}
	sender := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		// Nothing is read here, so no ping is answered with a pong, but a message goes out every 20 ms for a while.
		for range 40 {
			if ws.WriteMessage(websocket.BinaryMessage, []byte("x")) != nil {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		time.Sleep(2 * time.Second)
	}))
	t.Cleanup(sender.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(sender.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	conn := NewConn(ws, nil)
	t.Cleanup(func() { conn.Close() })
	conn.KeepAlive(20*time.Millisecond, 200*time.Millisecond)

	buf := make([]byte, 1)
	for i := range 40 {
		if _, err := conn.Read(buf); err != nil {
			t.Fatalf("message %d of a peer that keeps sending: %v", i, err)
		}
	}
	// Then the peer goes quiet, and the silence ends the connection.
	if _, err := conn.Read(buf); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after the peer fell silent: %v, want the connection to have been closed", err)
	}
}

func TestKeepAliveHoldsAConnectionThatKeepsAnswering(t *testing.T) {
	conn := dialConn(t, wsEcho(t, nil), nil)
	conn.KeepAlive(20*time.Millisecond, 200*time.Millisecond)
	result := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("a connection whose peer answers its pings failed after about the idle time: %v", err)
	case <-time.After(700 * time.Millisecond):
	}
	if _, err := conn.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatalf("read: %v", err)
	}
}

// tcpPair is the two ends of one connection, as the master and an agent hold the connection that the agent opened.
func tcpPair(t *testing.T) (master, agent net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- conn
	}()
	agent, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	master = <-accepted
	if master == nil {
		t.Fatal("no connection was accepted")
	}
	t.Cleanup(func() { master.Close(); agent.Close() })
	return master, agent
}

func TestServeConnAnswersTheClientOfTheMasterAsHTTP2(t *testing.T) {
	masterEnd, agentEnd := tcpPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan struct{})
	go func() {
		defer close(served)
		ServeConn(ctx, agentEnd, &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_, _ = w.Write([]byte(r.Proto + " " + r.URL.Path + " " + string(body)))
		})})
	}()

	cc, err := NewClientConn(ctx, masterEnd)
	if err != nil {
		t.Fatalf("NewClientConn: %v", err)
	}
	// Several requests at once, and a body each way, over the one connection.
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodPost, "http://agent.invalid/v1/echo", strings.NewReader(strings.Repeat("x", 100000+i)))
			resp, err := cc.RoundTrip(req)
			if err != nil {
				t.Errorf("request %d: %v", i, err)
				return
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(resp.Body)
			if want := "HTTP/2.0 /v1/echo " + strings.Repeat("x", 100000+i); string(got) != want {
				t.Errorf("request %d answered %.40q (%d bytes), want the echo of its body over HTTP/2", i, got, len(got))
			}
		}()
	}
	wg.Wait()

	if err := cc.Err(); err != nil {
		t.Fatalf("the connection failed: %v", err)
	}
	_ = cc.Close()
	select {
	case <-served:
	case <-time.After(10 * time.Second):
		t.Fatal("ServeConn did not return when the master closed the connection")
	}
}

func TestServeConnEndsWithItsContextAndCancelsTheRequestsInFlight(t *testing.T) {
	masterEnd, agentEnd := tcpPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	inFlight := make(chan struct{})
	cancelled := make(chan struct{})
	served := make(chan struct{})
	go func() {
		defer close(served)
		ServeConn(ctx, agentEnd, &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(inFlight)
			<-r.Context().Done()
			close(cancelled)
		})})
	}()
	cc, err := NewClientConn(context.Background(), masterEnd)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		req, _ := http.NewRequest(http.MethodGet, "http://agent.invalid/v1/slow", nil)
		if resp, err := cc.RoundTrip(req); err == nil {
			resp.Body.Close()
		}
	}()
	<-inFlight
	cancel()
	for name, done := range map[string]chan struct{}{"the request in flight": cancelled, "ServeConn": served} {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatalf("%s did not end when the context of the agent did", name)
		}
	}
}

// muteConn drops what is written to it once muted, as a path that has gone dead without closing does.
type muteConn struct {
	net.Conn
	muted atomic.Bool
}

func (c *muteConn) Write(p []byte) (int, error) {
	if c.muted.Load() {
		return len(p), nil
	}
	return c.Conn.Write(p)
}

func TestClientConnDropsAnAgentThatStopsAnswering(t *testing.T) {
	masterEnd, agentEnd := tcpPair(t)
	agent := &muteConn{Conn: agentEnd}
	go ServeConn(context.Background(), agent, &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("here"))
	})})
	cc, err := newClientConn(context.Background(), masterEnd, 50*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	get := func() error {
		req, _ := http.NewRequest(http.MethodGet, "http://agent.invalid/v1/status", nil)
		resp, err := cc.RoundTrip(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(); err != nil {
		t.Fatalf("a request to an agent that answers: %v", err)
	}

	// An agent that is quiet but answers its pings is kept, over many times the time it takes to ask.
	time.Sleep(time.Second)
	if err := cc.Err(); err != nil {
		t.Fatalf("an agent that answers its pings was dropped: %v", err)
	}

	agent.muted.Store(true)
	deadline := time.Now().Add(10 * time.Second)
	for cc.Err() == nil {
		if time.Now().After(deadline) {
			t.Fatal("an agent that no longer answers was still held after 10 seconds")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := get(); err == nil {
		t.Fatal("a request went out on the connection of an agent that was dropped")
	}
}

func TestServeConnAnswersNoMoreRequestsAtOnceThanItAdvertises(t *testing.T) {
	masterEnd, agentEnd := tcpPair(t)
	var running, peak atomic.Int32
	release := make(chan struct{})
	go ServeConn(context.Background(), agentEnd, &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := running.Add(1)
		defer running.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		<-release
		_, _ = w.Write([]byte("done"))
	})})
	cc, err := NewClientConn(context.Background(), masterEnd)
	if err != nil {
		t.Fatal(err)
	}
	const requests = tunnelStreams + 8
	var wg sync.WaitGroup
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "http://agent.invalid/v1/slow", nil)
			resp, err := cc.RoundTrip(req)
			if err != nil {
				t.Errorf("request: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	deadline := time.Now().Add(10 * time.Second)
	for running.Load() < tunnelStreams {
		if time.Now().After(deadline) {
			t.Fatalf("only %d requests were running after 10 seconds", running.Load())
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // time enough for the rest to be let in, if they would be
	if got := peak.Load(); got != tunnelStreams {
		t.Errorf("%d requests ran at once, want the %d the agent advertises", got, tunnelStreams)
	}
	close(release)
	wg.Wait()
}
