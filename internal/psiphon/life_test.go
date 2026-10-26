package psiphon

import (
	"errors"
	"testing"
	"time"
)

func TestLifeBacksOffAProcessThatCannotStayUp(t *testing.T) {
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	var l life
	l.started(t0)

	// It ends five seconds in: that is a failed start, and the wait grows with every one.
	now := t0.Add(5 * time.Second)
	l.noteDown(now, "exit status 1")
	if l.delay != restartBackoff || !l.retryAt.Equal(now.Add(restartBackoff)) {
		t.Fatalf("after the first end: delay %v, retry at %v, want %v from %v", l.delay, l.retryAt, restartBackoff, now)
	}
	if l.lastErr != "exit status 1" {
		t.Fatalf("lastErr = %q, want the reason the process gave", l.lastErr)
	}

	// Looking again counts nothing twice.
	l.noteDown(now.Add(time.Second), "")
	if l.delay != restartBackoff {
		t.Fatalf("an end counted twice: delay %v", l.delay)
	}

	// Each failed attempt doubles the wait, up to the cap.
	last := l.delay
	for range 10 {
		l.failed(now, errors.New("port 19101 is held by another process"))
		if l.delay < last || l.delay > maxRestartBackoff {
			t.Fatalf("delay went from %v to %v, want it to grow and stop at %v", last, l.delay, maxRestartBackoff)
		}
		last = l.delay
	}
	if l.delay != maxRestartBackoff {
		t.Fatalf("delay = %v after many failures, want the cap %v", l.delay, maxRestartBackoff)
	}
	if l.lastErr != "port 19101 is held by another process" {
		t.Fatalf("lastErr = %q", l.lastErr)
	}
}

func TestLifeRestartsAtOnceAProcessThatRanLongEnough(t *testing.T) {
	t0 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	l := life{delay: maxRestartBackoff}
	l.started(t0)

	now := t0.Add(stableAfter + time.Second)
	l.noteDown(now, "")
	if l.delay != 0 || !l.retryAt.Equal(now) {
		t.Fatalf("delay %v, retry at %v, want an immediate restart and the earlier failures forgotten", l.delay, l.retryAt)
	}
}

func TestLifeStartClearsTheLastError(t *testing.T) {
	l := life{lastErr: "old"}
	l.started(time.Now())
	if l.lastErr != "" || l.startedAt.IsZero() {
		t.Fatalf("life after a start = %+v", l)
	}
}
