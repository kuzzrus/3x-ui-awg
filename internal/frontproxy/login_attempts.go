package frontproxy

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// loginAttempts tracks failed logins per source IP for one decoy template, so it locks out repeated guesses instead of accepting unlimited attempts.
type loginAttempts struct {
	mu    sync.Mutex
	state map[string]*loginAttemptState
	// now is overridden in tests to avoid real sleeps around a ban window.
	now func() time.Time
}

type loginAttemptState struct {
	fails       int
	lockedUntil time.Time
}

func newLoginAttempts() *loginAttempts {
	return &loginAttempts{state: make(map[string]*loginAttemptState), now: time.Now}
}

// clientKey is the direct TCP peer, never a header that a client could change. A loopback peer has no key: the REALITY
// fallback relays every visitor from there, so it is all of them at once, and one scanner would lock out the rest.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return ""
	}
	return host
}

// check reports whether key is currently locked out, without counting a new
// attempt. An expired lockout is cleared so a fresh sequence can start.
func (a *loginAttempts) check(key string) (locked bool, until time.Time) {
	if key == "" {
		return false, time.Time{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.state[key]
	if s == nil || s.lockedUntil.IsZero() {
		return false, time.Time{}
	}
	now := a.now()
	if now.Before(s.lockedUntil) {
		return true, s.lockedUntil
	}
	s.fails, s.lockedUntil = 0, time.Time{}
	return false, time.Time{}
}

// fail records one failed attempt for key, locking it out for banDuration once threshold consecutive failures accumulate.
// An already-expired lockout is cleared first, so this attempt starts a fresh count rather than immediately re-locking.
func (a *loginAttempts) fail(key string, threshold int, banDuration time.Duration) (locked bool, until time.Time) {
	if key == "" {
		return false, time.Time{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	s := a.state[key]
	if s == nil {
		s = &loginAttemptState{}
		a.state[key] = s
	}
	if !s.lockedUntil.IsZero() {
		if now.Before(s.lockedUntil) {
			return true, s.lockedUntil
		}
		s.fails, s.lockedUntil = 0, time.Time{}
	}
	s.fails++
	if s.fails >= threshold {
		s.lockedUntil = now.Add(banDuration)
		return true, s.lockedUntil
	}
	return false, time.Time{}
}
