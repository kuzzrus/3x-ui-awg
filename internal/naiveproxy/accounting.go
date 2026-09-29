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
	Tag   string
	Email string
	Up    int64
	Down  int64
}

type counters struct {
	up   int64
	down int64
}

// meter turns Caddy's access log into per-user byte counters, as forward_proxy has no
// stats endpoint. A CONNECT tunnel is one log line, written when the tunnel closes.
type meter struct {
	mu       sync.Mutex
	tag      string
	partial  []byte
	overflow bool
	pending  map[string]*counters
}

func newMeter(tag string) *meter {
	return &meter{tag: tag, pending: map[string]*counters{}}
}

func (m *meter) setTag(tag string) {
	m.mu.Lock()
	m.tag = tag
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
	c := m.pending[rec.UserID]
	if c == nil {
		c = &counters{}
		m.pending[rec.UserID] = c
	}
	c.up += max(rec.BytesRead, 0)
	c.down += max(rec.Size, 0)
}

// drain hands over everything recorded since the last call, sorted by email.
func (m *meter) drain() []Traffic {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil
	}
	out := make([]Traffic, 0, len(m.pending))
	for email, c := range m.pending {
		out = append(out, Traffic{Tag: m.tag, Email: email, Up: c.up, Down: c.down})
	}
	clear(m.pending)
	slices.SortFunc(out, func(a, b Traffic) int { return strings.Compare(a.Email, b.Email) })
	return out
}
