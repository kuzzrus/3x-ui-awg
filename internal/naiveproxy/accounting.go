package naiveproxy

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"sync"
)

// maxAccessLine bounds one buffered access-log line. Filtered lines are a few
// hundred bytes, so anything near this is not one worth parsing.
const maxAccessLine = 64 << 10

// invalidUserPrefix is what forward_proxy puts in user_id for a failed login.
const invalidUserPrefix = "invalid:"

// Traffic is one client's byte delta since the last CollectTraffic (Up is client to
// target, Down the reverse). Email is Caddy's user id, which is the client's email.
type Traffic struct {
	Tag    string
	Email  string
	Routed bool // written while Caddy dialed out through Xray, whose bridge already counts it under Tag
	Up     int64
	Down   int64
}

type counters struct {
	up   int64
	down int64
}

// usageKey separates what one user moved under each routing, so a switch never re-labels old bytes.
type usageKey struct {
	email  string
	routed bool
}

// meter turns Caddy's access log into per-user byte counters, as forward_proxy has no
// stats endpoint. A CONNECT tunnel is one log line, written when the tunnel closes.
type meter struct {
	mu       sync.Mutex
	tag      string
	routed   bool // the routing of the process now writing, stamped on each line as it arrives
	partial  []byte
	overflow bool
	pending  map[usageKey]*counters
}

func newMeter(tag string) *meter {
	return &meter{tag: tag, pending: map[usageKey]*counters{}}
}

func (m *meter) setTag(tag string) {
	m.mu.Lock()
	m.tag = tag
	m.mu.Unlock()
}

// setRouted labels the lines written from now on; bytes already recorded keep their label.
func (m *meter) setRouted(routed bool) {
	m.mu.Lock()
	m.routed = routed
	m.mu.Unlock()
}

// Write consumes Caddy's stdout a chunk at a time and records every complete
// access line. It never fails, so a bad line can not stall the child's pipe.
func (m *meter) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if !m.overflow {
				m.partial = append(m.partial, p...)
				if len(m.partial) > maxAccessLine {
					m.partial, m.overflow = m.partial[:0], true
				}
			}
			break
		}
		if m.overflow {
			m.overflow = false
		} else {
			m.partial = append(m.partial, p[:i]...)
			if len(m.partial) <= maxAccessLine {
				m.recordLocked(m.partial)
			}
		}
		m.partial = m.partial[:0]
		p = p[i+1:]
	}
	return n, nil
}

// discardPartial drops a half-written line when its process is gone, so it can
// never be glued onto the first line of the next one.
func (m *meter) discardPartial() {
	m.mu.Lock()
	m.partial, m.overflow = m.partial[:0], false
	m.mu.Unlock()
}

func (m *meter) recordLocked(line []byte) {
	var rec struct {
		Logger    string `json:"logger"`
		UserID    string `json:"user_id"`
		BytesRead int64  `json:"bytes_read"`
		Size      int64  `json:"size"`
	}
	if json.Unmarshal(line, &rec) != nil || !strings.HasPrefix(rec.Logger, "http.log.access") {
		return
	}
	if rec.UserID == "" || strings.HasPrefix(rec.UserID, invalidUserPrefix) {
		return
	}
	if rec.BytesRead <= 0 && rec.Size <= 0 {
		return
	}
	key := usageKey{email: rec.UserID, routed: m.routed}
	c := m.pending[key]
	if c == nil {
		c = &counters{}
		m.pending[key] = c
	}
	c.up += max(rec.BytesRead, 0)
	c.down += max(rec.Size, 0)
}

// drain hands over everything recorded since the last call, sorted by email, unrouted first.
func (m *meter) drain() []Traffic {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := make([]Traffic, 0, len(m.pending))
	for key, c := range m.pending {
		out = append(out, Traffic{Tag: m.tag, Email: key.email, Routed: key.routed, Up: c.up, Down: c.down})
	}
	clear(m.pending)
	slices.SortFunc(out, func(a, b Traffic) int {
		if c := strings.Compare(a.Email, b.Email); c != 0 {
			return c
		}
		if a.Routed != b.Routed {
			if a.Routed {
				return 1
			}
			return -1
		}
		return 0
	})
	return out
}
