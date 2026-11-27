package hostfw

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Exec runs a command that changes the host, outside the sandbox of the panel, and returns what it printed; name says what runs.
type Exec func(ctx context.Context, name string, argv, env []string) ([]byte, error)

// DefaultWindow is the longest a hole stays open when nobody closes it: a certificate round takes a minute or two.
const DefaultWindow = 5 * time.Minute

// changeTimeout bounds one change of the firewall, a transient unit started for it included.
const changeTimeout = 20 * time.Second

// sweepRetry is how long the sweep leaves a port alone after it tried to take its rule out.
const sweepRetry = time.Minute

// Opener lets a port through ufw for a while, and takes the hole out again. Only a rule that it added itself, marked by its
// comment, is ever taken out; a rule of the admin's for the port is left as it is, and the port is then not touched at all.
type Opener struct {
	exec   Exec
	detect func(context.Context) Status
	rules  func() ([]ufwRule, error)
	lookup func() (string, error)
	now    func() time.Time
	window time.Duration

	// mu is held through every change, so that the timer, a release and a sweep never meet in the middle of one.
	mu   sync.Mutex
	held map[int]*time.Timer
	// swept is when the sweep last tried a port, so that a delete that keeps failing is not tried, and logged, every few seconds.
	swept map[int]time.Time
}

// NewOpener returns an Opener that makes its changes with exec.
func NewOpener(exec Exec) *Opener {
	return &Opener{
		exec:   exec,
		detect: func(ctx context.Context) Status { return Detect(ctx, ExecRunner) },
		rules:  readUFWRules,
		lookup: func() (string, error) { return LookPath("ufw") },
		now:    time.Now,
		window: DefaultWindow,
		held:   map[int]*time.Timer{},
		swept:  map[int]time.Time{},
	}
}

func tcp(port int) string { return strconv.Itoa(port) + "/tcp" }

// pastDue says whether a rule due at that unix time may be taken out: its time has gone by, or is so far off that no round of this
// panel can have set it.
func (o *Opener) pastDue(due int64) bool {
	now := o.now()
	return due <= now.Unix() || due > now.Add(2*o.window).Unix()
}

func (o *Opener) anyLive(due []int64) bool {
	return slices.ContainsFunc(due, func(at int64) bool { return !o.pastDue(at) })
}

// Open lets tcp port through when ufw is the host's firewall and holds no rule of the admin's for it, and returns the func that
// takes the hole out again, never nil. The error is for the log, not a reason to stop; a rule that a round of long ago left is taken over.
func (o *Opener) Open(ctx context.Context, port int) (func(), error) {
	nothing := func() {}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, held := o.held[port]; held {
		return nothing, nil
	}
	if !slices.Contains(o.detect(ctx).Managers, UFW) {
		return nothing, nil
	}
	rules, err := o.rules()
	if err != nil {
		return nothing, fmt.Errorf("hostfw: the rules of ufw cannot be read: %w", err)
	}
	conflict, ours := ufwPort(rules, port)
	switch {
	case o.anyLive(ours):
		// A round that is still on, here or in another panel of the host, holds the port open, and its owner takes the rule out.
		logger.Infof("hostfw: ufw holds a rule for %s that a round of the panel still needs, leaving it", tcp(port))
		return nothing, nil
	case len(ours) > 0:
		logger.Infof("hostfw: ufw holds a rule for %s from a round that ended long ago, taking it over", tcp(port))
	case conflict:
		logger.Infof("hostfw: ufw has a rule for %s already, leaving it as it is", tcp(port))
		return nothing, nil
	default:
		if err := o.add(ctx, port); err != nil {
			return nothing, err
		}
	}
	o.held[port] = time.AfterFunc(o.window, func() {
		logger.Warningf("hostfw: %s was still let through after %s, taking it out", tcp(port), o.window)
		o.closePort(port)
	})
	return func() { o.closePort(port) }, nil
}

func (o *Opener) add(ctx context.Context, port int) error {
	path, err := o.lookup()
	if err != nil {
		return fmt.Errorf("hostfw: ufw was not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, changeTimeout)
	defer cancel()
	mark := fmt.Sprintf("%s %d", ufwMark, o.now().Add(o.window).Unix())
	out, err := o.exec(ctx, "ufw allow "+tcp(port), []string{path, "allow", tcp(port), "comment", mark}, []string{"LC_ALL=C"})
	if err != nil {
		return fmt.Errorf("hostfw: ufw allow %s: %w", tcp(port), err)
	}
	// "Rule added" is what a rule that was not there says. A rule of the admin's that came in after the files were read is one
	// that ufw rewrote, and what it was cannot be put back.
	if said := string(out); strings.Contains(said, "Rule updated") || strings.Contains(said, "Skipping adding existing rule") {
		logger.Warningf("hostfw: ufw took the new rule for %s for one it had, and changed it: %s", tcp(port), strings.TrimSpace(said))
	}
	return nil
}

// closePort takes the hole out, once: a release that comes after the timer, or the timer after a release, finds nothing held.
func (o *Opener) closePort(port int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	timer, held := o.held[port]
	if !held {
		return
	}
	timer.Stop()
	delete(o.held, port)
	ctx, cancel := context.WithTimeout(context.Background(), changeTimeout)
	defer cancel()
	o.removeOurs(ctx, port)
}

// removeOurs deletes the panel's own rule for the port, if the files still show it: a rule that the admin has replaced since is
// theirs now, and a rule that is gone needs no deleting.
func (o *Opener) removeOurs(ctx context.Context, port int) {
	rules, err := o.rules()
	if err != nil {
		logger.Warningf("hostfw: the rules of ufw cannot be read, so the hole for %s stays: %v", tcp(port), err)
		return
	}
	if _, ours := ufwPort(rules, port); len(ours) == 0 {
		return
	}
	path, err := o.lookup()
	if err != nil {
		logger.Warningf("hostfw: ufw was not found, so the hole for %s stays: %v", tcp(port), err)
		return
	}
	if _, err := o.exec(ctx, "ufw delete allow "+tcp(port), []string{path, "--force", "delete", "allow", tcp(port)}, []string{"LC_ALL=C"}); err != nil {
		logger.Warningf("hostfw: ufw delete allow %s failed, so the hole stays until the next sweep: %v", tcp(port), err)
		return
	}
	logger.Infof("hostfw: the hole for %s is taken out of ufw", tcp(port))
}

// Sweep takes out the panel's own rules that nothing holds and whose time is past: what a panel that stopped in the middle of a
// round left behind. It does not wait while a change is under way, and says nothing on a host that has no ufw.
func (o *Opener) Sweep(ctx context.Context) {
	if !o.mu.TryLock() {
		return
	}
	defer o.mu.Unlock()
	rules, err := o.rules()
	if err != nil {
		return
	}
	due := map[int][]int64{}
	for _, own := range ufwOwn(rules) {
		due[own.port] = append(due[own.port], own.expiry)
	}
	ports := make([]int, 0, len(due))
	for port := range due {
		ports = append(ports, port)
	}
	slices.Sort(ports)
	for _, port := range ports {
		if _, held := o.held[port]; held || o.anyLive(due[port]) {
			continue
		}
		if last, tried := o.swept[port]; tried && o.now().Sub(last) < sweepRetry {
			continue
		}
		o.swept[port] = o.now()
		logger.Warningf("hostfw: ufw holds a rule of the panel for %s that nothing holds open any more, taking it out", tcp(port))
		ctx, cancel := context.WithTimeout(ctx, changeTimeout)
		o.removeOurs(ctx, port)
		cancel()
	}
}
