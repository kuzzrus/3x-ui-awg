package frontproxy

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

// fakeOpener notes what it was asked, in order: "open 80" for a hole, "close 80" for the one that was taken out again.
type fakeOpener struct {
	mu       sync.Mutex
	events   []string
	err      error
	deadline time.Duration
}

func (f *fakeOpener) Open(ctx context.Context, port int) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "open "+strconv.Itoa(port))
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	if f.err != nil {
		return func() {}, f.err
	}
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.events = append(f.events, "close "+strconv.Itoa(port))
	}, nil
}

func (f *fakeOpener) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// feed returns a func that hands certmagic events, in order, to the hook of a round of its own on opener.
func feed(t *testing.T, opener PortOpener) func(events ...string) {
	t.Helper()
	hook := certOnEvent(certmagic.NewDefault(), "example.test", newFirewallRound(opener))
	return func(events ...string) {
		t.Helper()
		for _, e := range events {
			if err := hook(t.Context(), e, map[string]any{"identifier": "example.test", "error": errors.New("boom")}); err != nil {
				t.Fatalf("%s: the hook returned %v, and an error aborts the attempt at the certificate", e, err)
			}
		}
	}
}

func expectEvents(t *testing.T, f *fakeOpener, want ...string) {
	t.Helper()
	if got := f.seen(); !slices.Equal(got, want) {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

func TestTheChallengePortIsOpenFromTheStartOfAnAttemptToItsSuccess(t *testing.T) {
	resetCertStatus()
	f := &fakeOpener{}
	send := feed(t, f)
	send("cert_obtaining")
	expectEvents(t, f, "open 80")
	send("cert_obtained")
	expectEvents(t, f, "open 80", "close 80")
}

func TestTheChallengePortIsTakenOutWhenTheAttemptFails(t *testing.T) {
	resetCertStatus()
	f := &fakeOpener{}
	feed(t, f)("cert_obtaining", "cert_failed")
	expectEvents(t, f, "open 80", "close 80")
	if status := CurrentCertStatus(); status.State != CertStateFailed || status.Error != "boom" {
		t.Errorf("status = %+v, want the failure still shown as it was", status)
	}
}

// Between two attempts there is a back-off of minutes or hours, which the port must not stay open through.
func TestEachAttemptOpensThePortAgainAndClosesItAfter(t *testing.T) {
	f := &fakeOpener{}
	feed(t, f)("cert_obtaining", "cert_failed", "cert_obtaining", "cert_failed")
	expectEvents(t, f, "open 80", "close 80", "open 80", "close 80")
}

// An attempt that ended without an event, such as one that failed saving what it got, must not leave its hole open behind the next.
func TestAnAttemptThatNeverSaidItEndedIsEndedBeforeTheNextBegins(t *testing.T) {
	f := &fakeOpener{}
	feed(t, f)("cert_obtaining", "cert_obtaining", "cert_obtained")
	expectEvents(t, f, "open 80", "close 80", "open 80", "close 80")
}

func TestAnEndWithNothingOpenDoesNothing(t *testing.T) {
	f := &fakeOpener{}
	feed(t, f)("cert_obtained", "cert_failed")
	expectEvents(t, f)
}

func TestTheRoundGoesOnWhenThePortCannotBeLetThrough(t *testing.T) {
	resetCertStatus()
	f := &fakeOpener{err: errors.New("ufw is not readable")}
	feed(t, f)("cert_obtaining", "cert_failed")
	// What cannot be let through is not held, so there is nothing to take out: the events went through without an error.
	expectEvents(t, f, "open 80")
}

func TestOtherEventsLeaveThePortAlone(t *testing.T) {
	f := &fakeOpener{}
	feed(t, f)("tls_get_certificate", "cached_managed_cert", "cert_ocsp_revoked", "cert_cleaned")
	expectEvents(t, f)
}

func TestAHookWithNoOpenerOrNoRoundOnlyTracksTheStatus(t *testing.T) {
	resetCertStatus()
	for _, round := range []*firewallRound{nil, newFirewallRound(nil)} {
		hook := certOnEvent(certmagic.NewDefault(), "example.test", round)
		for _, e := range []string{"cert_obtaining", "cert_failed", "cert_obtained"} {
			if err := hook(t.Context(), e, map[string]any{}); err != nil {
				t.Fatalf("%s: %v", e, err)
			}
		}
	}
}

// A certificate attempt waits for the firewall, so the wait has a limit.
func TestTheWaitForTheFirewallIsBounded(t *testing.T) {
	f := &fakeOpener{}
	feed(t, f)("cert_obtaining")
	if f.deadline <= 0 || f.deadline > openTimeout {
		t.Fatalf("the deadline given to Open is %s, want a positive one of at most %s", f.deadline, openTimeout)
	}
}

func startManualDoor(t *testing.T, port int) (*Manager, error) {
	t.Helper()
	certFile, keyFile, _ := writeTestCert(t)
	m := &Manager{}
	err := m.Start(Options{
		Listen: "127.0.0.1", Port: port, Routing: Config{PanelBasePath: "/p/", PanelPort: 1},
		TLS: TLSSettings{Mode: CertManual, Domain: "example.test", CertFile: certFile, KeyFile: keyFile},
	})
	return m, err
}

func TestStopTakesOutTheHoleOfTheRunningRound(t *testing.T) {
	m, err := startManualDoor(t, freePort(t))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	f := &fakeOpener{}
	round := newFirewallRound(f)
	round.begin()
	setActiveRound(round)
	t.Cleanup(func() { setActiveRound(nil) })
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	expectEvents(t, f, "open 80", "close 80")
}

func TestAStartThatCannotListenTakesOutTheHole(t *testing.T) {
	taken := freePort(t)
	held, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(taken))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	f := &fakeOpener{}
	round := newFirewallRound(f)
	round.begin()
	setActiveRound(round)
	t.Cleanup(func() { setActiveRound(nil) })
	m, err := startManualDoor(t, taken)
	if err == nil {
		_ = m.Stop()
		t.Fatal("Start succeeded on a port that is taken")
	}
	expectEvents(t, f, "open 80", "close 80")
}

// The whole way: certmagic itself starts an attempt, cannot reach its CA, and fails, and the hole is opened for it and taken out.
func TestAnAutoCertificateAttemptOpensAndClosesTheFirewall(t *testing.T) {
	resetCertStatus()
	acme, storage := certmagic.DefaultACME.CA, certmagic.Default.Storage
	t.Cleanup(func() {
		certmagic.DefaultACME.CA, certmagic.Default.Storage = acme, storage
		setActiveRound(nil)
		resetCertStatus()
	})
	// A CA that nothing listens on: the attempt starts, and fails at once.
	certmagic.DefaultACME.CA = "http://127.0.0.1:1/directory"
	f := &fakeOpener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := autoTLSConfig(ctx, TLSSettings{Mode: CertAuto, Domain: "example.org", StorageDir: t.TempDir(), Firewall: f}); err != nil {
		t.Fatalf("autoTLSConfig: %v", err)
	}
	activeRoundMu.Lock()
	remembered := activeRound
	activeRoundMu.Unlock()
	if remembered == nil || remembered.opener != PortOpener(f) {
		t.Fatal("the round of the reverse proxy was not remembered, so that Stop could end it")
	}
	deadline := time.Now().Add(20 * time.Second)
	for len(f.seen()) < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("events after 20 s = %q, want the port opened and taken out by the first attempt", f.seen())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := f.seen()[:2]; !slices.Equal(got, []string{"open 80", "close 80"}) {
		t.Fatalf("events = %q, want the port opened, then taken out when the attempt failed", got)
	}
}
