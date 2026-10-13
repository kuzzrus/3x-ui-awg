package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// fakeProgram is a helper program a test steers: whether it is on the machine, whether it runs, and
// what installing and starting it do.
type fakeProgram struct {
	mu          sync.Mutex
	installed   bool
	running     bool
	result      string
	installErr  error
	startErr    error
	installGate chan struct{} // an install waits for it to be closed
	installs    int
	starts      int
	stops       int
}

func (p *fakeProgram) Installed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installed
}

func (p *fakeProgram) Install(ctx context.Context) error {
	p.mu.Lock()
	p.installs++
	gate := p.installGate
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.installErr != nil {
		return p.installErr
	}
	p.installed = true
	return nil
}

func (p *fakeProgram) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	if p.startErr != nil {
		return p.startErr
	}
	p.running = true
	return nil
}

func (p *fakeProgram) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	p.running = false
	return nil
}

func (p *fakeProgram) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *fakeProgram) Result() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.result
}

// die is the program exiting on its own, with the last thing it said.
func (p *fakeProgram) die(result string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.running, p.result = false, result
}

func (p *fakeProgram) counts() (installs, starts, stops int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.installs, p.starts, p.stops
}

type hostFixture struct {
	t     *testing.T
	state *State
	prog  *fakeProgram
	host  *sidecarHost
	clock time.Time
}

func newHostFixture(t *testing.T, prog *fakeProgram) *hostFixture {
	t.Helper()
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &hostFixture{t: t, state: state, prog: prog, clock: time.Unix(1_700_000_000, 0)}
	f.host = f.hostOn(state)
	return f
}

func (f *hostFixture) hostOn(state *State) *sidecarHost {
	host := newSidecarHost(state, map[string]sidecar{agentproto.SidecarTor: f.prog})
	host.now = func() time.Time { return f.clock }
	return host
}

func (f *hostFixture) set(names ...string) agentproto.Sidecars {
	f.t.Helper()
	view, err := f.host.Set(names)
	if err != nil {
		f.t.Fatalf("Set(%q): %v", names, err)
	}
	return view
}

func (f *hostFixture) pass() { f.host.tend(context.Background(), agentproto.SidecarTor, f.prog) }

func (f *hostFixture) stateOfTor() agentproto.SidecarState {
	f.t.Helper()
	for _, one := range f.host.States() {
		if one.Name == agentproto.SidecarTor {
			return one
		}
	}
	f.t.Fatalf("states %+v do not list tor", f.host.States())
	return agentproto.SidecarState{}
}

func TestSidecarHostInstallsAndStartsWhatIsListed(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{})

	view := f.set(agentproto.SidecarTor)
	if view.Revision != agentproto.SidecarsRevision([]string{agentproto.SidecarTor}) || !slices.Equal(view.Enabled, []string{agentproto.SidecarTor}) {
		t.Fatalf("answer = %+v, want the list that was set", view)
	}
	if got := f.stateOfTor().State; got != agentproto.SidecarPending {
		t.Fatalf("state before the host looks = %q, want pending", got)
	}

	f.pass()
	installs, starts, _ := f.prog.counts()
	if installs != 1 || starts != 1 || f.stateOfTor().State != agentproto.SidecarRunning {
		t.Fatalf("installs %d, starts %d, state %+v; want one install, one start, running", installs, starts, f.stateOfTor())
	}

	f.pass()
	if installs, starts, _ := f.prog.counts(); installs != 1 || starts != 1 {
		t.Fatalf("a pass over a running program installed %d and started %d times", installs, starts)
	}
}

func TestSidecarHostDoesNotInstallWhatIsThere(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)
	f.pass()
	if installs, starts, _ := f.prog.counts(); installs != 0 || starts != 1 {
		t.Fatalf("installs %d, starts %d; want the program started without an install", installs, starts)
	}
}

func TestSidecarHostSaysWhatARunningProgramSaid(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)
	f.pass()

	f.prog.mu.Lock()
	f.prog.result = "Bootstrapped 45% (loading_descriptors)"
	f.prog.mu.Unlock()
	if got := f.stateOfTor().Message; got != "Bootstrapped 45% (loading_descriptors)" {
		t.Fatalf("message = %q, want what the program said last, without a pass in between", got)
	}
}

func TestSidecarHostStopsWhatIsNoLongerListed(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)
	f.pass()

	view := f.set()
	if view.Revision != "" || len(view.Enabled) != 0 || view.States == nil || len(view.States) != 0 {
		t.Fatalf("answer to an empty list = %+v, want no revision, no programs and an empty, not absent, list of states", view)
	}
	f.pass()
	if _, _, stops := f.prog.counts(); stops != 1 || f.prog.Running() {
		t.Fatalf("stops = %d, running = %v; want the program stopped once", stops, f.prog.Running())
	}
	f.pass()
	if _, _, stops := f.prog.counts(); stops != 1 {
		t.Fatalf("a pass over a stopped program stopped it again: %d stops", stops)
	}
}

func TestSidecarHostStartsAProgramThatDiedAfterAWhile(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)
	f.pass()

	f.prog.die("[err] Could not bind to 127.0.0.1:19050: Address already in use")
	f.pass()
	got := f.stateOfTor()
	if got.State != agentproto.SidecarFailed || !strings.Contains(got.Message, "Address already in use") {
		t.Fatalf("state after the program died = %+v, want failed with what it said", got)
	}
	if _, starts, _ := f.prog.counts(); starts != 1 {
		t.Fatalf("starts = %d right after it died, want it left alone for a while", starts)
	}

	f.clock = f.clock.Add(sidecarRestartAfter - time.Second)
	f.pass()
	if _, starts, _ := f.prog.counts(); starts != 1 {
		t.Fatalf("starts = %d before the wait is over", starts)
	}
	f.clock = f.clock.Add(2 * time.Second)
	f.pass()
	if _, starts, _ := f.prog.counts(); starts != 2 || f.stateOfTor().State != agentproto.SidecarRunning {
		t.Fatalf("starts = %d, state %+v; want it running again after the wait", starts, f.stateOfTor())
	}
}

func TestSidecarHostBacksOffAProgramThatWillNotStart(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true, startErr: errors.New("exec: permission denied")})
	f.set(agentproto.SidecarTor)
	f.pass()
	got := f.stateOfTor()
	if got.State != agentproto.SidecarFailed || got.Message != "exec: permission denied" {
		t.Fatalf("state = %+v, want failed with the error", got)
	}
	f.pass()
	f.pass()
	if _, starts, _ := f.prog.counts(); starts != 1 {
		t.Fatalf("starts = %d, want one attempt for the whole wait", starts)
	}
}

func TestSidecarHostLeavesAFailedInstallAloneForLong(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installErr: errors.New("apt-get install -y tor: exit status 100: E: Unable to locate package tor")})
	f.set(agentproto.SidecarTor)
	f.pass()
	got := f.stateOfTor()
	if got.State != agentproto.SidecarFailed || !strings.Contains(got.Message, "Unable to locate package tor") {
		t.Fatalf("state = %+v, want failed with why", got)
	}

	f.clock = f.clock.Add(sidecarRestartAfter * 2)
	f.pass()
	if installs, _, _ := f.prog.counts(); installs != 1 {
		t.Fatalf("installs = %d a minute after a failed one, want the package manager left alone", installs)
	}
	f.clock = f.clock.Add(sidecarInstallRetryAfter)
	f.pass()
	if installs, _, _ := f.prog.counts(); installs != 2 {
		t.Fatalf("installs = %d after the long wait, want another attempt", installs)
	}
}

func TestSidecarHostTurningItOffAndOnTriesAtOnce(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installErr: errors.New("no network")})
	f.set(agentproto.SidecarTor)
	f.pass()
	f.prog.mu.Lock()
	f.prog.installErr = nil
	f.prog.mu.Unlock()

	f.set()
	f.pass()
	f.set(agentproto.SidecarTor)
	f.pass()
	if got := f.stateOfTor(); got.State != agentproto.SidecarRunning {
		t.Fatalf("state = %+v, want the program installed and running without waiting out the failure", got)
	}
}

func TestSidecarHostKeepsTheListAcrossARestart(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)

	again := f.hostOn(f.state)
	if again.Revision() != agentproto.SidecarsRevision([]string{agentproto.SidecarTor}) {
		t.Fatalf("revision after a restart = %q, want the one of the stored list", again.Revision())
	}
	again.tend(context.Background(), agentproto.SidecarTor, f.prog)
	if _, starts, _ := f.prog.counts(); starts != 1 {
		t.Fatalf("starts = %d, want the stored program started with the master out of reach", starts)
	}
}

func TestSidecarHostRefusesAProgramItDoesNotKnow(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	f.set(agentproto.SidecarTor)

	_, err := f.host.Set([]string{agentproto.SidecarTor, "no-such-program"})
	var unknown *agentproto.UnknownSidecarError
	if !errors.As(err, &unknown) || unknown.Name != "no-such-program" {
		t.Fatalf("Set error = %v, want the unknown program named", err)
	}
	if stored, err := f.state.Sidecars(); err != nil || !slices.Equal(stored, []string{agentproto.SidecarTor}) {
		t.Fatalf("stored list = %q, %v; want the one from before the refusal", stored, err)
	}
	if !slices.Equal(f.host.View().Enabled, []string{agentproto.SidecarTor}) {
		t.Fatalf("list after a refusal = %q, want it unchanged", f.host.View().Enabled)
	}
}

func TestStateIgnoresAStoredProgramThisBuildDoesNotKnow(t *testing.T) {
	dir := t.TempDir()
	state, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sidecarsFile), []byte(`{"enabled":["tor","gone"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := state.Sidecars()
	if err != nil || !slices.Equal(got, []string{agentproto.SidecarTor}) {
		t.Fatalf("Sidecars() = %q, %v; want the known one only", got, err)
	}

	if err := os.WriteFile(filepath.Join(dir, sidecarsFile), []byte(`{"enabled":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Sidecars(); err == nil {
		t.Fatal("an unreadable list was taken for a list")
	}
	if host := newSidecarHost(state, defaultSidecars()); host.Revision() != "" {
		t.Fatalf("a host started on an unreadable list runs %q, want none until the master says", host.View().Enabled)
	}
}

// An install takes minutes, and the master's request must not wait for it.
func TestSidecarHostAnswersWhileInstalling(t *testing.T) {
	prog := &fakeProgram{installGate: make(chan struct{})}
	f := newHostFixture(t, prog)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.host.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	f.set(agentproto.SidecarTor)
	waitForHost(t, "the install to start", func() bool { return f.stateOfTor().State == agentproto.SidecarInstalling })

	answered := make(chan agentproto.Sidecars, 1)
	go func() {
		view, _ := f.host.Set(nil)
		answered <- view
	}()
	select {
	case view := <-answered:
		if view.Revision != "" {
			t.Fatalf("answer = %+v, want the empty list", view)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a new list waited for the install to end")
	}

	close(prog.installGate)
	waitForHost(t, "the install to end", func() bool { return prog.Installed() })
	// The list was emptied during the install, so the program that was installed is not started.
	time.Sleep(50 * time.Millisecond)
	if _, starts, _ := prog.counts(); starts != 0 {
		t.Fatalf("starts = %d, want a program that is no longer listed left stopped", starts)
	}
}

// An install takes minutes, and a program that waits for another one's is left unsupervised for as long.
func TestSidecarHostDoesNotLetOneInstallHoldUpAnotherProgram(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tor := &fakeProgram{installed: true}
	psiphon := &fakeProgram{installGate: make(chan struct{})}
	host := newSidecarHost(state, map[string]sidecar{agentproto.SidecarTor: tor, agentproto.SidecarPsiphon: psiphon})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		host.Run(ctx)
	}()
	var release sync.Once
	t.Cleanup(func() {
		release.Do(func() { close(psiphon.installGate) })
		cancel()
		<-done
	})
	stateOf := func(name string) string {
		for _, one := range host.States() {
			if one.Name == name {
				return one.State
			}
		}
		return ""
	}

	if _, err := host.Set([]string{agentproto.SidecarTor, agentproto.SidecarPsiphon}); err != nil {
		t.Fatal(err)
	}
	waitForHost(t, "Psiphon to be downloading", func() bool { return stateOf(agentproto.SidecarPsiphon) == agentproto.SidecarInstalling })
	waitForHost(t, "Tor to run beside the download", tor.Running)

	// Taking Tor off the list stops it at once, though the download is still on.
	if _, err := host.Set([]string{agentproto.SidecarPsiphon}); err != nil {
		t.Fatal(err)
	}
	waitForHost(t, "Tor to stop beside the download", func() bool { return !tor.Running() })
	if got := stateOf(agentproto.SidecarPsiphon); got != agentproto.SidecarInstalling {
		t.Fatalf("Psiphon is %q, want it still downloading", got)
	}

	release.Do(func() { close(psiphon.installGate) })
	waitForHost(t, "Psiphon to run once it is there", func() bool { return stateOf(agentproto.SidecarPsiphon) == agentproto.SidecarRunning })
}

// The decoy runs AdGuard Home for itself: the master does not list it, and does not see it.
func TestSidecarHostRunsWhatTheAgentRequiresAndLetsItGo(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	needed := &fakeProgram{installed: true}
	host := newSidecarHost(state, map[string]sidecar{adguardName: needed})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		host.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if got, _ := host.StateOf(adguardName); got != agentproto.SidecarPending {
		t.Fatalf("a program nobody asked for stands %q before it is looked at, want pending", got)
	}

	host.Require(adguardName, true)
	waitForHost(t, "the required program to run", needed.Running)
	if got, _ := host.StateOf(adguardName); got != agentproto.SidecarRunning {
		t.Fatalf("StateOf = %q, want running", got)
	}
	if states := host.States(); len(states) != 0 {
		t.Fatalf("states = %+v, want the master to see none: it did not list the program", states)
	}
	if view := host.View(); view.Revision != "" || len(view.Enabled) != 0 {
		t.Fatalf("view = %+v, want the list of the master untouched", view)
	}

	host.Require(adguardName, false)
	waitForHost(t, "the program to stop once nothing needs it", func() bool { return !needed.Running() })
}

func TestSidecarHostStopsItsProgramsWhenTheContextEnds(t *testing.T) {
	f := newHostFixture(t, &fakeProgram{installed: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.host.Run(ctx)
	}()
	f.set(agentproto.SidecarTor)
	waitForHost(t, "the program to run", func() bool { return f.prog.Running() })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if f.prog.Running() {
		t.Fatal("the program was left running when the agent stopped")
	}
}

func TestSidecarHostGivesUpAnInstallWhenTheContextEnds(t *testing.T) {
	prog := &fakeProgram{installGate: make(chan struct{})}
	f := newHostFixture(t, prog)
	f.set(agentproto.SidecarTor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.host.tend(ctx, agentproto.SidecarTor, prog)
	}()
	waitForHost(t, "the install to start", func() bool { return f.stateOfTor().State == agentproto.SidecarInstalling })

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a pass held up the agent's shutdown for the whole install")
	}
	if _, starts, _ := prog.counts(); starts != 0 {
		t.Fatalf("starts = %d, want nothing started once the agent is stopping", starts)
	}
}

// A name the protocol lists and the host has no program for would be taken by the agent and never run.
func TestEveryKnownSidecarHasAProgramAndNothingElseButWhatTheAgentAsksFor(t *testing.T) {
	programs := defaultSidecars()
	for _, name := range agentproto.KnownSidecars {
		if programs[name] == nil {
			t.Errorf("no program runs %q, which the protocol lists", name)
		}
	}
	if want := len(agentproto.KnownSidecars) + 1; len(programs) != want || programs[adguardName] == nil {
		t.Errorf("the host has %d programs, want the %d of the protocol and AdGuard Home, which the decoy asks for", len(programs), want)
	}
	if slices.Contains(agentproto.KnownSidecars, adguardName) {
		t.Errorf("%q is a name of the protocol, which would let the master list a program the decoy owns", adguardName)
	}
}

func TestTidyTorLineDropsTheDateAndTheNoticeLevel(t *testing.T) {
	for line, want := range map[string]string{
		"Oct 11 14:27:10.000 [notice] Bootstrapped 10% (conn_done): Connected to a relay": "Bootstrapped 10% (conn_done): Connected to a relay",
		"Oct  1 04:07:10.123 [warn] You are running Tor as root.":                         "[warn] You are running Tor as root.",
		"Oct 11 14:27:10 [err] Could not bind to 127.0.0.1:19050: Address already in use": "[err] Could not bind to 127.0.0.1:19050: Address already in use",
		"exit status 1": "exit status 1",
		"":              "",
	} {
		if got := tidyTorLine(line); got != want {
			t.Errorf("tidyTorLine(%q) = %q, want %q", line, got, want)
		}
	}
}

func TestTailOfKeepsTheEndOnOneLine(t *testing.T) {
	if got := tailOf("  a \n\n b\tc  "); got != "a b c" {
		t.Fatalf("tailOf = %q, want one line", got)
	}
	long := strings.Repeat("x", 500) + " the reason"
	got := tailOf(long)
	if !strings.HasSuffix(got, " the reason") || len([]rune(got)) != maxSidecarMessage+1 || !strings.HasPrefix(got, "…") {
		t.Fatalf("tailOf of a long text = %q (%d runes), want its last %d with a mark that it was cut", got, len([]rune(got)), maxSidecarMessage)
	}
	if got := tailOf(strings.Repeat("я", 400)); len([]rune(got)) != maxSidecarMessage+1 {
		t.Fatalf("tailOf cut a text of wide characters to %d runes, want %d", len([]rune(got)), maxSidecarMessage+1)
	}
}

func waitForHost(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
