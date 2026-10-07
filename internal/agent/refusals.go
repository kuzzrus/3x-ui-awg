package agent

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const refusalLogEvery = time.Minute

// refusalLog writes why a request got the bare 404 to the log, once a minute per reason,
// so a client that knows the server name cannot flood it. It never writes the secret.
type refusalLog struct {
	mu      sync.Mutex
	last    map[string]time.Time
	skipped map[string]int
	now     func() time.Time                 // time.Now unless a test sets it
	warnf   func(format string, args ...any) // logger.Warningf unless a test sets it
}

func (l *refusalLog) note(r *http.Request, reason string) {
	now, warnf := time.Now, logger.Warningf
	if l.now != nil {
		now = l.now
	}
	if l.warnf != nil {
		warnf = l.warnf
	}

	at := now()
	l.mu.Lock()
	if l.last == nil {
		l.last, l.skipped = map[string]time.Time{}, map[string]int{}
	}
	if last, seen := l.last[reason]; seen && at.Sub(last) < refusalLogEvery {
		l.skipped[reason]++
		l.mu.Unlock()
		return
	}
	skipped := l.skipped[reason]
	l.last[reason], l.skipped[reason] = at, 0
	l.mu.Unlock()

	more := ""
	if skipped > 0 {
		more = fmt.Sprintf(" (%d more like it since the last line)", skipped)
	}
	warnf("agent: refused %s %q from %s: %s%s", r.Method, r.URL.Path, r.RemoteAddr, reason, more)
}
