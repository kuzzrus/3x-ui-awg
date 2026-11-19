package tproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// stubDial replaces the probe's dial for one test.
func stubDial(t *testing.T, fn func(ctx context.Context, addr string) error) {
	t.Helper()
	orig := dcProbeDial
	dcProbeDial = fn
	t.Cleanup(func() { dcProbeDial = orig })
}

// dialRecorder stands in for the probe's dial: it counts the dials and answers with the error that is set (none: the address answers).
type dialRecorder struct {
	mu  sync.Mutex
	err error
	n   int
}

func (d *dialRecorder) dial(context.Context, string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	return d.err
}

func (d *dialRecorder) set(err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.err = err
}

func (d *dialRecorder) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

func recordDials(t *testing.T, err error) *dialRecorder {
	t.Helper()
	d := &dialRecorder{err: err}
	stubDial(t, d.dial)
	return d
}

// fakeClock is the probe's clock for a test, which moves only when it is told to.
type fakeClock struct{ now time.Time }

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func stubClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{now: time.Unix(1_800_000_000, 0)}
	orig := dcProbeNow
	dcProbeNow = func() time.Time { return c.now }
	t.Cleanup(func() { dcProbeNow = orig })
	return c
}

// telegramConf is a proxy-multi.conf whose proxy_for lines list addrs.
func telegramConf(addrs ...string) string {
	var b strings.Builder
	b.WriteString(strings.Repeat("# padding\n", 10))
	b.WriteString("default 2;\n")
	for i, a := range addrs {
		fmt.Fprintf(&b, "proxy_for %d %s;\n", i+1, a)
	}
	return b.String()
}

// useProxyMultiConf puts conf where the probe reads it, in a bin folder of its own.
func useProxyMultiConf(t *testing.T, conf string) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		t.Fatalf("create %s: %v", dir(), err)
	}
	if err := os.WriteFile(proxyMultiConfPath(), []byte(conf), 0o600); err != nil {
		t.Fatalf("write proxy-multi.conf: %v", err)
	}
}

// loggedCount counts the lines of the panel log, at the level or worse, that hold part.
func loggedCount(level, part string) int {
	n := 0
	for _, line := range logger.GetLogs(10000, level) {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

func TestDcProbeAddrsReadsTheProxyForLines(t *testing.T) {
	conf := strings.Join([]string{
		"# comment 10.0.0.1:1",
		"default 2;",
		"proxy_for 1 149.154.175.50:8888;",
		"proxy_for -1 149.154.175.50:8888;",         // the same address under another id
		"proxy_for 2 149.154.167.51:8888;\r",        // a file with CRLF line ends
		"proxy_for 3 [2001:b28:f23d:f001::d]:8888;", // the engine is started without -6
		"proxy_for 4 example.com:8888;",             // not an address
		"proxy_for 5 91.108.4.1:0;",                 // no such port
		"proxy_for 6 91.108.4.2:65536;",             // no such port
		"proxy_for 7 91.108.4.3:http;",              // no such port
		"proxy_for 8",                               // nothing after the id
		"proxy_for 9 91.108.4.4;",                   // no port
		"proxy 10 91.108.4.5:8888;",                 // not a proxy_for line
		"  proxy_for 11 91.108.56.1:443  ",          // no semicolon, and spaces around
		"proxy_for 12 91.108.56.2:1;",               // the lowest port
		"proxy_for 13 91.108.56.3:65535;",           // the highest port
	}, "\n")
	want := []string{"149.154.175.50:8888", "149.154.167.51:8888", "91.108.56.1:443", "91.108.56.2:1", "91.108.56.3:65535"}
	if got := dcProbeAddrs([]byte(conf)); !slices.Equal(got, want) {
		t.Errorf("dcProbeAddrs = %v, want %v", got, want)
	}
}

func TestDcProbeAddrsThinsALongListToEvenSteps(t *testing.T) {
	cases := []struct {
		n    int
		want []int
	}{
		{0, nil},
		{1, []int{0}},
		{3, []int{0, 1, 2}},
		{6, []int{0, 1, 2, 3, 4, 5}},
		{7, []int{0, 1, 2, 3, 4, 5}},
		{12, []int{0, 2, 4, 6, 8, 10}},
		{20, []int{0, 3, 6, 10, 13, 16}},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.n), func(t *testing.T) {
			var conf strings.Builder
			for i := range c.n {
				fmt.Fprintf(&conf, "proxy_for %d 10.0.0.%d:8888;\n", i, i)
			}
			var want []string
			for _, i := range c.want {
				want = append(want, fmt.Sprintf("10.0.0.%d:8888", i))
			}
			if got := dcProbeAddrs([]byte(conf.String())); !slices.Equal(got, want) {
				t.Errorf("dcProbeAddrs of %d addresses = %v, want %v", c.n, got, want)
			}
		})
	}
}

func TestDcProbeDialConnectsAndLetsGo(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		if c, err := ln.Accept(); err == nil {
			accepted <- c
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dcProbeDial(ctx, ln.Addr().String()); err != nil {
		t.Fatalf("dial of a listening address: %v", err)
	}
	select {
	case c := <-accepted:
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Errorf("the listener's side saw %v, want the connection closed (EOF): a probe must not leave connections open", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the listener saw no connection")
	}
}

func TestDcProbeDialFailsWhereNothingListens(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dcProbeDial(ctx, addr); err == nil {
		t.Error("dial of an address nothing listens on succeeded")
	}
}

// The engine is started without -6, so an address it could not use is not one that proves anything.
func TestDcProbeDialDoesNotGoOverIPv6(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback on this host: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := dcProbeDial(ctx, ln.Addr().String()); err == nil {
		t.Error("dial of an IPv6 address succeeded, want the probe to use IPv4 only")
	}
}

func TestProbeDataCentersFirstAnswerEndsTheRound(t *testing.T) {
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	stubDial(t, func(_ context.Context, addr string) error {
		if addr == "10.0.0.1:8888" {
			return nil
		}
		// An address that never answers and takes no notice of the round's deadline.
		<-release
		return errors.New("too late")
	})
	done := make(chan bool, 1)
	go func() {
		ok, _ := probeDataCenters(context.Background(), []string{"10.0.0.2:8888", "10.0.0.1:8888"})
		done <- ok
	}()
	select {
	case ok := <-done:
		if !ok {
			t.Error("probeDataCenters = false although an address answered")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the round waited for an address that does not answer, after another one had")
	}
}

func TestProbeDataCentersDialsEveryAddressAtOnce(t *testing.T) {
	addrs := []string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888"}
	var started sync.WaitGroup
	started.Add(len(addrs))
	// Each dial answers only once all of them have begun, which a round that dials one after the other never gets to.
	stubDial(t, func(ctx context.Context, _ string) error {
		started.Done()
		all := make(chan struct{})
		go func() { started.Wait(); close(all) }()
		select {
		case <-all:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if ok, detail := probeDataCenters(context.Background(), addrs); !ok {
		t.Errorf("probeDataCenters = false (%s), want the dials to run side by side", detail)
	}
}

func TestProbeDataCentersBoundsTheRound(t *testing.T) {
	var mu sync.Mutex
	var deadline time.Time
	stubDial(t, func(ctx context.Context, _ string) error {
		d, _ := ctx.Deadline()
		mu.Lock()
		deadline = d
		mu.Unlock()
		return nil
	})
	start := time.Now()
	probeDataCenters(context.Background(), []string{"10.0.0.1:8888"})
	mu.Lock()
	defer mu.Unlock()
	if deadline.IsZero() {
		t.Fatal("a dial was given no deadline")
	}
	if left := deadline.Sub(start); left < dcProbeTimeout || left > dcProbeTimeout+2*time.Second {
		t.Errorf("a dial's deadline is %v after the round began, want %v", left, dcProbeTimeout)
	}
}

func TestProbeDataCentersGivesUpAtTheDeadline(t *testing.T) {
	stubDial(t, func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	})
	type verdict struct {
		ok     bool
		detail string
	}
	done := make(chan verdict, 1)
	go func() {
		ok, detail := probeDataCenters(context.Background(), []string{"10.0.0.1:8888", "10.0.0.2:8888"})
		done <- verdict{ok, detail}
	}()
	select {
	case v := <-done:
		if v.ok {
			t.Error("probeDataCenters = true although no address answered")
		}
		if !strings.Contains(v.detail, "deadline exceeded") {
			t.Errorf("detail = %q, want it to say what the dial ended with", v.detail)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the round did not end at its deadline")
	}
}

func TestProbeDataCentersNamesTheErrorOfTheFirstListedAddress(t *testing.T) {
	addrs := []string{"10.0.0.1:8888", "10.0.0.2:8888", "10.0.0.3:8888"}
	var others sync.WaitGroup
	others.Add(2)
	stubDial(t, func(_ context.Context, addr string) error {
		if addr == addrs[0] {
			// Fails after the others, so that its error is the last to come and still the one that is told.
			others.Wait()
			time.Sleep(50 * time.Millisecond)
			return errors.New("err-first")
		}
		defer others.Done()
		return errors.New("err-" + addr)
	})
	ok, detail := probeDataCenters(context.Background(), addrs)
	if ok {
		t.Fatal("probeDataCenters = true although every address failed")
	}
	for _, want := range []string{strings.Join(addrs, ", "), "err-first", dcProbeTimeout.String()} {
		if !strings.Contains(detail, want) {
			t.Errorf("detail = %q, want it to hold %q", detail, want)
		}
	}
	for _, unwanted := range []string{"err-10.0.0.2", "err-10.0.0.3"} {
		if strings.Contains(detail, unwanted) {
			t.Errorf("detail = %q, want the error of the first listed address only, not %q", detail, unwanted)
		}
	}
}

func TestReachabilityKnowsNothingFromAConfWithNothingToProbe(t *testing.T) {
	// Only an IPv6 address: nothing the engine could be tried with, so no verdict and no refusal.
	useProxyMultiConf(t, telegramConf("[2001:db8::1]:8888"))
	dials := recordDials(t, errors.New("no route to host"))
	var r dcReachability
	r.refresh(context.Background(), true)
	if n := dials.count(); n != 0 {
		t.Errorf("%d dial(s) for a conf with no IPv4 address, want none", n)
	}
	if err := r.err(); err != nil {
		t.Errorf("err = %v, want a start never to be refused without a verdict", err)
	}
}

func TestReachabilityKnowsNothingWithoutAConf(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	dials := recordDials(t, errors.New("no route to host"))
	var r dcReachability
	r.refresh(context.Background(), true)
	if n := dials.count(); n != 0 {
		t.Errorf("%d dial(s) without a conf, want none", n)
	}
	if err := r.err(); err != nil {
		t.Errorf("err = %v, want a start never to be refused without a verdict", err)
	}
}

func TestReachabilityRefusesAStartWhenNoAddressAnswers(t *testing.T) {
	useProxyMultiConf(t, telegramConf("203.0.113.9:8888"))
	recordDials(t, errors.New("no route to host"))
	var r dcReachability
	r.refresh(context.Background(), false)
	err := r.err()
	if !errors.Is(err, errTelegramUnreachable) {
		t.Fatalf("err = %v, want it to wrap errTelegramUnreachable", err)
	}
	for _, want := range []string{"203.0.113.9:8888", "no route to host", "start by itself"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to hold %q", err, want)
		}
	}
}

func TestReachabilityLetsAStartThroughWhenAnAddressAnswers(t *testing.T) {
	useProxyMultiConf(t, telegramConf("203.0.113.9:8888", "203.0.113.10:8888"))
	stubDial(t, func(_ context.Context, addr string) error {
		if addr == "203.0.113.10:8888" {
			return nil
		}
		return errors.New("no route to host")
	})
	var r dcReachability
	r.refresh(context.Background(), false)
	if err := r.err(); err != nil {
		t.Errorf("err = %v, want one address that answers to be enough", err)
	}
}

func TestReachabilityVerdictsAgeOut(t *testing.T) {
	cases := []struct {
		name     string
		answers  bool
		age      time.Duration
		force    bool
		wantDial bool
	}{
		{"a success is kept", true, dcProbeOKTTL - time.Second, false, false},
		{"a success is looked at again when its time is up", true, dcProbeOKTTL, false, true},
		{"a success is not looked at again by force", true, dcProbeOKTTL - time.Second, true, false},
		{"a first failure is kept", false, dcProbeFailMin - time.Second, false, false},
		{"a first failure is looked at again when its time is up", false, dcProbeFailMin, false, true},
		{"a first failure is looked at again by force", false, dcProbeFailMin - time.Second, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			useProxyMultiConf(t, telegramConf("203.0.113.9:8888"))
			clock := stubClock(t)
			dials := recordDials(t, nil)
			if !c.answers {
				dials.set(errors.New("no route to host"))
			}
			var r dcReachability
			r.refresh(context.Background(), false)
			if n := dials.count(); n != 1 {
				t.Fatalf("%d dial(s) for a first look, want 1", n)
			}
			clock.advance(c.age)
			r.refresh(context.Background(), c.force)
			if looked := dials.count() == 2; looked != c.wantDial {
				t.Errorf("looked again = %v, want %v", looked, c.wantDial)
			}
		})
	}
}

// A host that has just booted may have no uplink yet: its first failure is looked at again soon, and the later ones ever less often.
func TestReachabilityKeepsEachFailureInARowTwiceAsLong(t *testing.T) {
	useProxyMultiConf(t, telegramConf("203.0.113.9:8888"))
	clock := stubClock(t)
	dials := recordDials(t, errors.New("no route to host"))
	var r dcReachability
	r.refresh(context.Background(), false)
	kept := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, want := range kept {
		before := dials.count()
		clock.advance(want - time.Second)
		r.refresh(context.Background(), false)
		if got := dials.count(); got != before {
			t.Fatalf("failure %d was looked at again %v after it, want it kept for %v", i+1, want-time.Second, want)
		}
		clock.advance(time.Second)
		r.refresh(context.Background(), false)
		if got := dials.count(); got != before+1 {
			t.Fatalf("failure %d was not looked at again when its %v were up", i+1, want)
		}
	}
}

func TestReachabilityASuccessStartsTheFailuresOver(t *testing.T) {
	useProxyMultiConf(t, telegramConf("203.0.113.9:8888"))
	clock := stubClock(t)
	dials := recordDials(t, errors.New("no route to host"))
	var r dcReachability
	for range 3 {
		r.refresh(context.Background(), false)
		clock.advance(dcProbeFailTTL)
	}
	dials.set(nil)
	r.refresh(context.Background(), false)
	clock.advance(dcProbeOKTTL)
	dials.set(errors.New("no route to host"))
	r.refresh(context.Background(), false)

	before := dials.count()
	clock.advance(dcProbeFailMin - time.Second)
	r.refresh(context.Background(), false)
	if dials.count() != before {
		t.Fatal("a first failure after a success was looked at again too soon")
	}
	clock.advance(time.Second)
	r.refresh(context.Background(), false)
	if dials.count() != before+1 {
		t.Errorf("a first failure after a success was not looked at again after %v: the earlier failures still count", dcProbeFailMin)
	}
}

func TestReachabilitySaysOnlyAChangeOfVerdict(t *testing.T) {
	marker := "198.51.100.23:8888"
	useProxyMultiConf(t, telegramConf(marker))
	clock := stubClock(t)
	dials := recordDials(t, errors.New("no route to host"))
	warned := func() int { return loggedCount("warning", marker) }
	answered := func() int { return loggedCount("info", "answer again") }
	w0, a0 := warned(), answered()
	var r dcReachability
	look := func(wantWarned, wantAnswered int, what string) {
		t.Helper()
		r.refresh(context.Background(), false)
		if got := warned() - w0; got != wantWarned {
			t.Errorf("%s: %d warning(s) so far, want %d", what, got, wantWarned)
		}
		if got := answered() - a0; got != wantAnswered {
			t.Errorf("%s: %d notice(s) of an answer so far, want %d", what, got, wantAnswered)
		}
	}

	look(1, 0, "the first look finds no answer")
	clock.advance(dcProbeFailTTL)
	look(1, 0, "no answer again")
	dials.set(nil)
	clock.advance(dcProbeFailTTL)
	look(1, 1, "the address answers")
	clock.advance(dcProbeOKTTL)
	look(1, 1, "answers again")
	dials.set(errors.New("no route to host"))
	clock.advance(dcProbeOKTTL)
	look(2, 1, "the address is lost")
}

func TestReachabilityStaysQuietWhenTheFirstVerdictIsGood(t *testing.T) {
	marker := "198.51.100.24:8888"
	useProxyMultiConf(t, telegramConf(marker))
	recordDials(t, nil)
	w0, i0, a0 := loggedCount("warning", marker), loggedCount("info", marker), loggedCount("info", "answer again")
	var r dcReachability
	r.refresh(context.Background(), false)
	if got := loggedCount("warning", marker) - w0; got != 0 {
		t.Errorf("%d warning(s) for a good first look, want none", got)
	}
	if got := loggedCount("info", marker) - i0; got != 0 {
		t.Errorf("%d line(s) for a good first look, want none", got)
	}
	if got := loggedCount("info", "answer again") - a0; got != 0 {
		t.Errorf("a good first look was said to be an answer again %d time(s)", got)
	}
}

// A tick of the reconcile job and an admin's save can both look at Telegram at once; the race detector is what judges this one.
func TestReachabilityIsSafeToUseFromSeveralGoroutines(t *testing.T) {
	useProxyMultiConf(t, telegramConf("203.0.113.9:8888"))
	var mu sync.Mutex
	calls := 0
	stubDial(t, func(context.Context, string) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if calls%2 == 0 {
			return errors.New("no route to host")
		}
		return nil
	})
	var r dcReachability
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			for i := range 20 {
				r.refresh(context.Background(), (g+i)%2 == 0)
				_ = r.err()
			}
		})
	}
	wg.Wait()
}
