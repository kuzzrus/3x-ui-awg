package hostfw

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// now is the time of the fake host, and ahead of it are the 300 seconds that a round may hold a port.
const (
	now     = 1_800_000_000
	dueNext = now + 300
)

// fakeUFW stands in for ufw and the host around it: its rule files are lines, and the two commands that the panel runs change them
// the way ufw does.
type fakeUFW struct {
	mu       sync.Mutex
	managers []string
	lines    []string
	calls    []string
	execErr  error
	rulesErr error
	lookErr  error
	clock    int64
	// say is what "ufw allow" prints; it has the words of a clean add unless a test changes it.
	say string
	// hold, when set, is waited for inside every command, after entered is told that one began.
	hold    chan struct{}
	entered chan struct{}
}

func newFakeUFW(rules ...string) *fakeUFW {
	return &fakeUFW{managers: []string{UFW}, lines: rules, say: "Rule added\nRule added (v6)\n", clock: now}
}

func (f *fakeUFW) opener() *Opener {
	o := NewOpener(f.exec)
	o.detect = func(context.Context) Status { return Status{Readable: true, Managers: f.managers} }
	o.rules = func() ([]ufwRule, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.rulesErr != nil {
			return nil, f.rulesErr
		}
		return parseUFWRules(strings.Join(f.lines, "\n")), nil
	}
	o.lookup = func() (string, error) {
		if f.lookErr != nil {
			return "", f.lookErr
		}
		return "/usr/sbin/ufw", nil
	}
	o.now = func() time.Time {
		f.mu.Lock()
		defer f.mu.Unlock()
		return time.Unix(f.clock, 0)
	}
	return o
}

func (f *fakeUFW) exec(ctx context.Context, name string, argv, env []string) ([]byte, error) {
	if f.hold != nil {
		if f.entered != nil {
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		<-f.hold
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name+" | "+strings.Join(argv, " ")+" | "+strings.Join(env, " "))
	if f.execErr != nil {
		return nil, f.execErr
	}
	if slices.Contains(argv, "delete") {
		port := strings.TrimSuffix(argv[len(argv)-1], "/tcp")
		f.lines = slices.DeleteFunc(f.lines, func(l string) bool {
			for _, r := range parseUFWRules(l) {
				if r.ours() && r.dport == port {
					return true
				}
			}
			return false
		})
		return []byte("Rule deleted\nRule deleted (v6)\n"), nil
	}
	port := strings.TrimSuffix(argv[2], "/tcp")
	f.lines = append(f.lines, both("allow", "tcp", port, "any", "in comment="+hex.EncodeToString([]byte(argv[4])))...)
	return []byte(f.say), nil
}

func (f *fakeUFW) called() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *fakeUFW) set(lines ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lines = lines
}

func (f *fakeUFW) at(unix int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = unix
}

func (f *fakeUFW) count(substr string) int {
	n := 0
	for _, c := range f.called() {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

var (
	addCall    = "ufw allow 80/tcp | /usr/sbin/ufw allow 80/tcp comment x-ui acme " + strconv.Itoa(dueNext) + " | LC_ALL=C"
	deleteCall = "ufw delete allow 80/tcp | /usr/sbin/ufw --force delete allow 80/tcp | LC_ALL=C"
)

func TestOpenAddsTheRuleAndReleaseTakesItOut(t *testing.T) {
	host := newFakeUFW()
	release, err := host.opener().Open(context.Background(), 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := host.called(); !slices.Equal(got, []string{addCall}) {
		t.Fatalf("commands after Open = %q, want just the add, with its mark", got)
	}
	release()
	if got := host.called(); !slices.Equal(got, []string{addCall, deleteCall}) {
		t.Fatalf("commands after release = %q, want the add and the delete", got)
	}
	release()
	if n := len(host.called()); n != 2 {
		t.Fatalf("a second release ran a command: %d commands", n)
	}
}

func TestOpenDoesNothingWhenUFWIsNotTheFirewall(t *testing.T) {
	for _, managers := range [][]string{nil, {Firewalld}} {
		host := newFakeUFW()
		host.managers = managers
		release, err := host.opener().Open(context.Background(), 80)
		release()
		if err != nil || len(host.called()) != 0 {
			t.Errorf("with managers %v: error %v, commands %q, want none of either", managers, err, host.called())
		}
	}
}

func TestOpenLeavesARuleOfTheAdminsAlone(t *testing.T) {
	for name, lines := range map[string][]string{
		"allow": both("allow", "tcp", "80", "any", "in"),
		"deny":  both("deny", "tcp", "80", "any", "in"),
		"limit": both("limit", "tcp", "80", "any", "in"),
		"range": both("allow", "tcp", "80:90", "any", "in"),
	} {
		t.Run(name, func(t *testing.T) {
			host := newFakeUFW(lines...)
			release, err := host.opener().Open(context.Background(), 80)
			release()
			if err != nil || len(host.called()) != 0 {
				t.Fatalf("error %v, commands %q, want the admin's rule left as it is, with no command", err, host.called())
			}
		})
	}
}

func TestOpenAddsItsRuleBesideARuleForOneAddress(t *testing.T) {
	host := newFakeUFW(tuple("allow", "tcp", "80", v4, "any", "1.2.3.4", "in"))
	release, err := host.opener().Open(context.Background(), 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	release()
	if got := host.called(); !slices.Equal(got, []string{addCall, deleteCall}) {
		t.Fatalf("commands = %q, want the add and the delete", got)
	}
}

func TestOpenTakesOverARuleThatARoundOfLongAgoLeft(t *testing.T) {
	for name, due := range map[string]int64{"past": now - 10, "so far off that no round can have set it": now + 100_000} {
		t.Run(name, func(t *testing.T) {
			host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", due))...)
			release, err := host.opener().Open(context.Background(), 80)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if n := len(host.called()); n != 0 {
				t.Fatalf("Open ran %d commands, want none: the rule is there", n)
			}
			release()
			if got := host.called(); !slices.Equal(got, []string{deleteCall}) {
				t.Fatalf("commands = %q, want only the delete", got)
			}
		})
	}
}

// A round that is still on, in another panel of the same host, owns its rule, and its time is not up.
func TestOpenLeavesTheRuleOfARoundThatIsStillOn(t *testing.T) {
	host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", dueNext-100))...)
	release, err := host.opener().Open(context.Background(), 80)
	release()
	if err != nil || len(host.called()) != 0 {
		t.Fatalf("error %v, commands %q, want the live rule left to its owner", err, host.called())
	}
}

func TestOpenWhileTheHoleIsOpenDoesNothingMore(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	first, _ := o.Open(context.Background(), 80)
	second, err := o.Open(context.Background(), 80)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	second()
	if n := len(host.called()); n != 1 {
		t.Fatalf("%d commands after the second Open and its release, want the one add", n)
	}
	first()
	if host.count("delete") != 1 {
		t.Fatalf("the first release did not take the rule out: %q", host.called())
	}
}

func TestReleaseLeavesARuleThatIsNotTheirsAnyMore(t *testing.T) {
	host := newFakeUFW()
	release, _ := host.opener().Open(context.Background(), 80)
	// The admin has put a rule of their own in the place of the panel's: it has no mark.
	host.set(both("allow", "tcp", "80", "any", "in")...)
	release()
	if host.count("delete") != 0 {
		t.Fatalf("the admin's rule was deleted: %q", host.called())
	}
}

func TestReleaseOfARuleThatIsGoneRunsNothing(t *testing.T) {
	host := newFakeUFW()
	release, _ := host.opener().Open(context.Background(), 80)
	host.set()
	release()
	if host.count("delete") != 0 {
		t.Fatalf("a delete ran for a rule that is not there: %q", host.called())
	}
}

func TestOpenReportsAChangeThatFailedAndHoldsNothing(t *testing.T) {
	host := newFakeUFW()
	host.execErr = errors.New("boom")
	o := host.opener()
	release, err := o.Open(context.Background(), 80)
	release()
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Open error = %v, want it to hold what failed", err)
	}
	host.execErr = nil
	if _, err := o.Open(context.Background(), 80); err != nil || host.count("allow") != 2 {
		t.Fatalf("a later Open: error %v, %d add attempts; a failed one must not look like an open hole", err, host.count("allow"))
	}
}

func TestOpenReportsRulesThatCannotBeRead(t *testing.T) {
	host := newFakeUFW()
	host.rulesErr = errors.New("permission denied")
	release, err := host.opener().Open(context.Background(), 80)
	release()
	if err == nil || !strings.Contains(err.Error(), "permission denied") || len(host.called()) != 0 {
		t.Fatalf("error %v, commands %q, want the failure, and no command run blind", err, host.called())
	}
}

func TestOpenReportsAMissingUFW(t *testing.T) {
	host := newFakeUFW()
	host.lookErr = errors.New("not found")
	release, err := host.opener().Open(context.Background(), 80)
	release()
	if err == nil || !strings.Contains(err.Error(), "ufw was not found") || len(host.called()) != 0 {
		t.Fatalf("error %v, commands %q, want ufw reported missing", err, host.called())
	}
}

// ufw that says it updated a rule has rewritten one of the admin's: it is told in the log, and the round goes on.
func TestOpenGoesOnWhenUFWSaysItRewroteARule(t *testing.T) {
	host := newFakeUFW()
	host.say = "Rule updated\nRule updated (v6)\n"
	release, err := host.opener().Open(context.Background(), 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	release()
	if host.count("delete") != 1 {
		t.Fatalf("commands = %q, want the round to go on to its delete", host.called())
	}
}

func TestTheHoleClosesItselfWhenTheWindowIsUp(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	o.window = 30 * time.Millisecond
	release, err := o.Open(context.Background(), 80)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for host.count("delete") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the hole was still open long after its window")
		}
		time.Sleep(5 * time.Millisecond)
	}
	release()
	if n := host.count("delete"); n != 1 {
		t.Fatalf("%d deletes, want the timer's and no more", n)
	}
}

func TestReleaseThenTheTimerRunsNothingMore(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	o.window = 40 * time.Millisecond
	release, _ := o.Open(context.Background(), 80)
	release()
	time.Sleep(150 * time.Millisecond)
	if n := host.count("delete"); n != 1 {
		t.Fatalf("%d deletes, want the release's and not the timer's too", n)
	}
}

func TestSweepTakesOutRulesOfThePanelThatNothingHoldsAndWhoseTimeIsPast(t *testing.T) {
	lines := slices.Concat(
		both("allow", "tcp", "8080", "any", markTail("in", now-5)),
		both("allow", "tcp", "80", "any", markTail("in", now-100)),
		both("allow", "tcp", "22", "any", "in"),
	)
	host := newFakeUFW(lines...)
	host.opener().Sweep(context.Background())
	want := []string{
		"ufw delete allow 80/tcp | /usr/sbin/ufw --force delete allow 80/tcp | LC_ALL=C",
		"ufw delete allow 8080/tcp | /usr/sbin/ufw --force delete allow 8080/tcp | LC_ALL=C",
	}
	if got := host.called(); !slices.Equal(got, want) {
		t.Fatalf("commands = %q, want %q", got, want)
	}
	if len(parseUFWRules(strings.Join(host.lines, "\n"))) != 2 {
		t.Fatalf("what is left = %q, want the admin's rule for 22 and nothing else", host.lines)
	}
}

// Another panel of the host may be in the middle of its round: its rule is due later, and is not a leftover yet.
func TestSweepLeavesARuleWhoseTimeHasNotComeAndTakesItWhenItHas(t *testing.T) {
	host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", now+100))...)
	o := host.opener()
	o.Sweep(context.Background())
	if host.count("delete") != 0 {
		t.Fatalf("the sweep took out a rule whose time had not come: %q", host.called())
	}
	host.at(now + 101)
	o.Sweep(context.Background())
	if host.count("delete") != 1 {
		t.Fatalf("commands = %q, want the rule taken out once its time was past", host.called())
	}
}

func TestSweepTakesARuleWhoseTimeIsSoFarOffThatNoRoundSetIt(t *testing.T) {
	host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", now+100_000))...)
	host.opener().Sweep(context.Background())
	if host.count("delete") != 1 {
		t.Fatalf("commands = %q, want the rule taken out", host.called())
	}
}

func TestSweepLeavesTheRuleOfARoundThatIsOn(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	release, _ := o.Open(context.Background(), 80)
	// Its own window is up by the clock, and the round holds it still: the hold is what matters, not the clock.
	host.at(now + 1000)
	o.Sweep(context.Background())
	if host.count("delete") != 0 {
		t.Fatalf("the sweep took out a hole that a round holds open: %q", host.called())
	}
	release()
}

func TestSweepDoesNotWaitForAChangeUnderWay(t *testing.T) {
	host := newFakeUFW()
	host.hold = make(chan struct{})
	host.entered = make(chan struct{}, 1)
	o := host.opener()
	opened := make(chan struct{})
	go func() {
		release, _ := o.Open(context.Background(), 80)
		release()
		close(opened)
	}()
	// Open is inside its command, which waits on hold, with the opener's lock held.
	select {
	case <-host.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Open never reached its command")
	}
	swept := make(chan struct{})
	go func() {
		o.Sweep(context.Background())
		close(swept)
	}()
	select {
	case <-swept:
	case <-time.After(2 * time.Second):
		t.Fatal("the sweep waited for a change that is under way")
	}
	close(host.hold)
	<-opened
}

func TestSweepIsSilentWhenTheRulesCannotBeRead(t *testing.T) {
	host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", now-100))...)
	host.rulesErr = errors.New("no such file")
	host.opener().Sweep(context.Background())
	if n := len(host.called()); n != 0 {
		t.Fatalf("%d commands, want none on a host whose rules cannot be read", n)
	}
}

func TestSweepKeepsTheRuleWhenTheDeleteFails(t *testing.T) {
	host := newFakeUFW(both("allow", "tcp", "80", "any", markTail("in", now-100))...)
	host.execErr = errors.New("boom")
	o := host.opener()
	o.Sweep(context.Background())
	host.execErr = nil
	// Not at once: a delete that keeps failing must not be tried, and logged, every few seconds.
	o.Sweep(context.Background())
	if host.count("delete") != 1 {
		t.Fatalf("commands = %q, want the second sweep to leave the port alone for a while", host.called())
	}
	host.at(now + int64(sweepRetry/time.Second) + 1)
	o.Sweep(context.Background())
	if host.count("delete") != 2 || len(host.lines) != 0 {
		t.Fatalf("commands = %q, rules = %q, want the delete tried again once the wait was over", host.called(), host.lines)
	}
}

func TestSweepLeavesWhatTheAdminAddedWithoutTheMark(t *testing.T) {
	host := newFakeUFW(slices.Concat(
		both("allow", "tcp", "80", "any", "in"),
		both("allow", "tcp", "8080", "any", "in comment=782d75692061636d65"),
	)...)
	host.opener().Sweep(context.Background())
	if n := len(host.called()); n != 0 {
		t.Fatalf("%d commands, want none: nothing here carries the mark and a time", n)
	}
}

func TestPortsOtherThanEightyAreHeldAndReleasedOnTheirOwn(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	r80, _ := o.Open(context.Background(), 80)
	r81, _ := o.Open(context.Background(), 81)
	r80()
	if host.count("delete allow 81") != 0 || host.count("delete allow 80/tcp") != 1 {
		t.Fatalf("commands = %q, want the delete of 80 only", host.called())
	}
	r81()
	if host.count("delete") != 2 {
		t.Fatalf("commands = %q, want both deleted", host.called())
	}
}

// A rule is past due once its time has come, and also when its time is further off than two windows of a round, which no round of
// this panel sets.
func TestWhenARuleIsPastDue(t *testing.T) {
	o := newFakeUFW().opener()
	for due, want := range map[int64]bool{
		now - 1:       true,
		now:           true,
		now + 1:       false,
		now + 300:     false,
		now + 600:     false,
		now + 601:     true,
		now + 100_000: true,
		0:             true,
	} {
		if got := o.pastDue(due); got != want {
			t.Errorf("pastDue(now%+d) = %v, want %v", due-now, got, want)
		}
	}
}

// The hold, not the rule, is what says that a port is open: a rule that was taken out from under a round is not added again by
// another Open, which would replace the timer of the first.
func TestOpenWhileHeldDoesNotAddAgainEvenIfTheRuleWasTakenOut(t *testing.T) {
	host := newFakeUFW()
	o := host.opener()
	first, _ := o.Open(context.Background(), 80)
	host.set()
	second, err := o.Open(context.Background(), 80)
	second()
	if err != nil || host.count("allow") != 1 {
		t.Fatalf("error %v, %d adds, want the one add of the first Open", err, host.count("allow"))
	}
	first()
}
