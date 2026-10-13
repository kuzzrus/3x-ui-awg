package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

const (
	pullTestNode = 7
	pullTestPath = "/base/agent/connect/7"
)

// fakeMaster is the other end of pull mode: a TLS server that upgrades the agent's request to a WebSocket and
// turns the connection into an HTTP/2 client, the way the panel does.
type fakeMaster struct {
	t       *testing.T
	srv     *httptest.Server
	secret  string
	refuse  atomic.Bool
	dialled atomic.Int32
	conns   chan *http.ClientConn
}

func newFakeMaster(t *testing.T) *fakeMaster {
	t.Helper()
	secret, err := agentproto.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeMaster{t: t, secret: secret, conns: make(chan *http.ClientConn, 8)}
	upgrader := websocket.Upgrader{Subprotocols: []string{agentproto.ConnectProtocol}}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.dialled.Add(1)
		if m.refuse.Load() || r.URL.Path != pullTestPath || !agentproto.CheckBearer(r.Header.Get("Authorization"), secret) {
			http.NotFound(w, r)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		cc, err := agentproto.NewClientConn(r.Context(), agentproto.NewConn(ws, nil))
		if err != nil {
			return
		}
		m.conns <- cc
	}))
	t.Cleanup(m.srv.Close)
	return m
}

func (m *fakeMaster) pin() string {
	sum := sha256.Sum256(m.srv.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

func (m *fakeMaster) bundle(pin string) *agentproto.Bundle {
	m.t.Helper()
	b, err := agentproto.NewPullBundle(m.srv.URL+"/base/", pullTestNode, m.secret, pin)
	if err != nil {
		m.t.Fatal(err)
	}
	return b
}

// next is the connection the agent opened next, or a failure when it does not within a few seconds.
func (m *fakeMaster) next() *http.ClientConn {
	m.t.Helper()
	select {
	case cc := <-m.conns:
		return cc
	case <-time.After(10 * time.Second):
		m.t.Fatal("the agent did not connect")
		return nil
	}
}

func get(t *testing.T, cc *http.ClientConn, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://agent.invalid"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

var pongHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("pong " + r.URL.Path + " " + r.Proto))
})

// runLoop runs the agent's side of pull mode in the background and stops it with the test.
func runLoop(t *testing.T, loop *pullLoop) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		loop.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the pull loop did not stop with its context")
		}
	})
}

func quickLoop(b *agentproto.Bundle, sleeps chan<- time.Duration) *pullLoop {
	return &pullLoop{
		bundle: b,
		server: func() *http.Server { return &http.Server{Handler: pongHandler} },
		dial:   dialMaster,
		sleep: func(ctx context.Context, d time.Duration) {
			if sleeps != nil {
				sleeps <- d
			}
			sleepCtx(ctx, time.Millisecond)
		},
		now: time.Now,
	}
}

func TestPullLoopAnswersTheMasterOverTheConnectionItDialled(t *testing.T) {
	m := newFakeMaster(t)
	runLoop(t, quickLoop(m.bundle(m.pin()), nil))

	cc := m.next()
	for _, path := range []string{"/v1/status", "/v1/stats"} {
		if status, body := get(t, cc, path); status != http.StatusOK || body != "pong "+path+" HTTP/2.0" {
			t.Fatalf("GET %s answered %d %q", path, status, body)
		}
	}
}

func TestPullLoopDialsAgainWhenTheConnectionEnds(t *testing.T) {
	m := newFakeMaster(t)
	runLoop(t, quickLoop(m.bundle(m.pin()), nil))

	first := m.next()
	if status, _ := get(t, first, "/v1/status"); status != http.StatusOK {
		t.Fatalf("first connection answered %d", status)
	}
	_ = first.Close()

	second := m.next()
	if status, _ := get(t, second, "/v1/status"); status != http.StatusOK {
		t.Fatalf("second connection answered %d", status)
	}
}

// A master that turns the agent away must not be hammered: the wait between dials grows, up to a limit.
func TestPullLoopWaitsLongerAfterEachFailureUpToALimit(t *testing.T) {
	m := newFakeMaster(t)
	m.refuse.Store(true)
	sleeps := make(chan time.Duration, 64)
	runLoop(t, quickLoop(m.bundle(m.pin()), sleeps))

	var waits []time.Duration
	for len(waits) < 8 {
		select {
		case d := <-sleeps:
			waits = append(waits, d)
		case <-time.After(10 * time.Second):
			t.Fatalf("the loop stopped dialing after %v", waits)
		}
	}
	for i, d := range waits {
		base := min(pullWaitMin<<i, pullWaitMax)
		if d < base || d > base+base/5 {
			t.Errorf("wait %d was %v, want %v plus up to a fifth of it (all: %v)", i, d, base, waits)
		}
	}
	if m.dialled.Load() < 8 {
		t.Fatalf("the master saw %d dials", m.dialled.Load())
	}
}

// What the agent waited for before a connection that held counts for nothing once it has held for a while.
func TestPullLoopStartsOverAfterAConnectionThatHeld(t *testing.T) {
	m := newFakeMaster(t)
	m.refuse.Store(true)
	sleeps := make(chan time.Duration, 64)
	var clock atomic.Int64
	loop := quickLoop(m.bundle(m.pin()), sleeps)
	loop.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	runLoop(t, loop)

	for range 3 {
		<-sleeps
	}
	m.refuse.Store(false)
	cc := m.next()
	// An answer shows the agent is serving, so the moment it began at was taken before the clock moved on.
	if status, _ := get(t, cc, "/v1/status"); status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}
	clock.Add(int64(pullStableAfter/time.Second) + 5)
	_ = cc.Close()
	for {
		select {
		case d := <-sleeps:
			if d > pullWaitMin+pullWaitMin/5 {
				continue
			}
			return
		case <-time.After(10 * time.Second):
			t.Fatal("the wait after a connection that held was never the shortest again")
		}
	}
}

func TestPullLoopStopsAtOnceWhenItsContextEnds(t *testing.T) {
	m := newFakeMaster(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	loop := quickLoop(m.bundle(m.pin()), nil)
	go func() {
		defer close(done)
		loop.run(ctx)
	}()
	cc := m.next()
	if status, _ := get(t, cc, "/v1/status"); status != http.StatusOK {
		t.Fatalf("answered %d", status)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not return after its context ended, with a connection open")
	}
	asked := make(chan error, 1)
	go func() {
		req, err := http.NewRequest(http.MethodGet, "http://agent.invalid/v1/status", nil)
		if err != nil {
			asked <- err
			return
		}
		resp, err := cc.RoundTrip(req)
		if err == nil {
			resp.Body.Close()
		}
		asked <- err
	}()
	select {
	case err := <-asked:
		if err == nil {
			t.Fatal("the master got an answer from an agent that stopped")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a request to an agent that stopped is still waiting")
	}
}

func TestDialMasterTrustsOnlyThePinWhenTheBundleHasOne(t *testing.T) {
	m := newFakeMaster(t)
	other := strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		pin  string
		fail string
	}{
		"the pin of its certificate": {pin: m.pin()},
		"another pin":                {pin: other, fail: "does not match the pin"},
		"no pin, a self-signed one":  {pin: "", fail: "certificate"},
	} {
		conn, err := dialMaster(context.Background(), m.bundle(tc.pin))
		if err == nil {
			conn.Close()
		}
		switch {
		case tc.fail == "" && err != nil:
			t.Errorf("%s: %v", name, err)
		case tc.fail == "":
			m.next()
		case err == nil || !strings.Contains(err.Error(), tc.fail):
			t.Errorf("%s: dial error %v, want one that says %q", name, err, tc.fail)
		}
	}
}

func TestDialMasterSaysWhyTheMasterTurnedItAway(t *testing.T) {
	m := newFakeMaster(t)
	m.refuse.Store(true)
	_, err := dialMaster(context.Background(), m.bundle(m.pin()))
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("error = %v, want one that says the master answered HTTP 404", err)
	}
	if strings.Contains(err.Error(), m.secret) {
		t.Fatal("the error carries the secret")
	}
}

func TestDialMasterRefusesAnAddressThatDoesNotTakeAnAgent(t *testing.T) {
	upgrader := websocket.Upgrader{} // names no subprotocol
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ws, err := upgrader.Upgrade(w, r, nil); err == nil {
			defer ws.Close()
			_, _, _ = ws.ReadMessage()
		}
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256(srv.Certificate().Raw)
	secret, _ := agentproto.NewSecret()
	b, err := agentproto.NewPullBundle(srv.URL+"/base/", pullTestNode, secret, hex.EncodeToString(sum[:]))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dialMaster(context.Background(), b); err == nil || !strings.Contains(err.Error(), "takes an agent") {
		t.Fatalf("error = %v, want one that says the address does not take an agent", err)
	}
}
