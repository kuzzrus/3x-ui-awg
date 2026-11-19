package tproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// An engine that cannot reach Telegram retries its data centers without a pause, and every attempt is a new connection (about
// 60 a second): so a start waits until one of them answers.
const (
	// dcProbeTimeout is how long one probe round waits: its dials run side by side, so it is also the longest a round takes.
	dcProbeTimeout = 750 * time.Millisecond
	// dcProbeLimit caps the addresses of a round, spread over the whole list so that the data centers are not all one subnet.
	dcProbeLimit = 6
	// dcProbeOKTTL is how long a success is kept.
	dcProbeOKTTL = 10 * time.Minute
	// dcProbeFailMin is how long a first failure is kept, as a host that has just booted needs; each one after it twice as long, up to dcProbeFailTTL.
	dcProbeFailMin = 15 * time.Second
	dcProbeFailTTL = 5 * time.Minute
)

// errTelegramUnreachable is wrapped by the error of a start that was refused; Reconcile does not log it again, since the verdict did.
var errTelegramUnreachable = errors.New("tproxy: Telegram's data centers are unreachable from this host")

// dcProbeDial and dcProbeNow are the test seams of a probe: a dial that ends in nil reached the address, and the clock ages a verdict.
var (
	dcProbeDial = func(ctx context.Context, addr string) error {
		// tcp4: the engine is started without -6, so an IPv6 route is of no use to it.
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", addr)
		if err != nil {
			return err
		}
		return conn.Close()
	}
	dcProbeNow = time.Now
)

// dcReachability is the last verdict on whether the engine's own dials can get to Telegram; the zero value knows nothing.
type dcReachability struct {
	mu     sync.Mutex
	known  bool
	ok     bool
	fails  int // failures in a row, which set how long the verdict is kept
	at     time.Time
	detail string
}

// dcProbeFailKept is how long a failure is kept after fails of them in a row: the first is looked at again soon, then ever less often.
func dcProbeFailKept(fails int) time.Duration {
	d := dcProbeFailMin
	for i := 1; i < fails && d < dcProbeFailTTL; i++ {
		d *= 2
	}
	return min(d, dcProbeFailTTL)
}

// refresh probes the data centers unless a verdict that is still fresh is held; force sets aside a held failure only, since a
// success needs no second look. A conf that lists no address to probe leaves the verdict unknown, which never refuses a start.
func (r *dcReachability) refresh(ctx context.Context, force bool) {
	r.mu.Lock()
	age := dcProbeNow().Sub(r.at)
	fresh := r.known && ((r.ok && age < dcProbeOKTTL) || (!r.ok && !force && age < dcProbeFailKept(r.fails)))
	r.mu.Unlock()
	if fresh {
		return
	}
	conf, err := os.ReadFile(proxyMultiConfPath())
	if err != nil {
		return
	}
	addrs := dcProbeAddrs(conf)
	if len(addrs) == 0 {
		return
	}
	ok, detail := probeDataCenters(ctx, addrs)
	r.record(ok, detail)
}

// record keeps a verdict and says it in the log when it differs from the one before; a verdict seen again says nothing, and neither
// does the first one when it is good.
func (r *dcReachability) record(ok bool, detail string) {
	r.mu.Lock()
	wasKnown, was := r.known, r.ok
	r.known, r.ok, r.at, r.detail = true, ok, dcProbeNow(), detail
	if ok {
		r.fails = 0
	} else {
		r.fails++
	}
	r.mu.Unlock()
	switch {
	case !ok && (!wasKnown || was):
		logger.Warningf("%v (%s); MTProxy is not started until one of them answers", errTelegramUnreachable, detail)
	case ok && wasKnown && !was:
		logger.Info("tproxy: Telegram's data centers answer again, MTProxy can start")
	}
}

// err is nil for a reachable or unknown verdict, and otherwise the reason that a start is refused.
func (r *dcReachability) err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.known || r.ok {
		return nil
	}
	return fmt.Errorf("%w (%s); MTProxy was not started and will start by itself once one of them answers", errTelegramUnreachable, r.detail)
}

// probeDataCenters dials every address at once and is done with the first that answers; when none does, detail says which were
// tried and why the first failed.
func probeDataCenters(ctx context.Context, addrs []string) (ok bool, detail string) {
	ctx, cancel := context.WithTimeout(ctx, dcProbeTimeout)
	defer cancel()
	type result struct {
		i   int
		err error
	}
	// Read here, not in the goroutines: those still running when the first answer comes may begin after a test has put the seam back.
	dial := dcProbeDial
	// Buffered for every dial, so that those still running when the first answer comes are not left blocked.
	results := make(chan result, len(addrs))
	for i, addr := range addrs {
		go func() { results <- result{i, dial(ctx, addr)} }()
	}
	errs := make([]error, len(addrs))
	for range addrs {
		res := <-results
		if res.err == nil {
			return true, ""
		}
		errs[res.i] = res.err
	}
	return false, fmt.Sprintf("no connection within %s to any of %s, the first failed with: %v", dcProbeTimeout, strings.Join(addrs, ", "), errs[0])
}

// dcProbeAddrs lists the IPv4 addresses of proxy-multi.conf's proxy_for lines, each once and in the order of the file, thinned to
// dcProbeLimit by taking them at even steps: the file lists the data centers one after the other, and the first few are not all.
func dcProbeAddrs(conf []byte) []string {
	var all []string
	seen := map[string]bool{}
	for _, line := range bytes.Split(conf, []byte("\n")) {
		fields := strings.Fields(string(line))
		if len(fields) < 3 || fields[0] != "proxy_for" {
			continue
		}
		addr := strings.TrimSuffix(fields[2], ";")
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host)
		if p, perr := strconv.Atoi(port); ip == nil || ip.To4() == nil || perr != nil || p < 1 || p > 65535 || seen[addr] {
			continue
		}
		seen[addr] = true
		all = append(all, addr)
	}
	if len(all) <= dcProbeLimit {
		return all
	}
	picked := make([]string, 0, dcProbeLimit)
	for i := range dcProbeLimit {
		picked = append(picked, all[i*len(all)/dcProbeLimit])
	}
	return picked
}
