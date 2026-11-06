//go:build !windows

package psiphon

import (
	"errors"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func listening(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// eventually waits for cond, which a started or killed process needs a moment to make true.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("gave up waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// newTestManager is a manager of its own, with its tunnels stopped when the test ends.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := newManager()
	t.Cleanup(m.StopAll)
	return m
}

func TestCountriesRunSideBySideEachOnItsOwnPort(t *testing.T) {
	useFakeCore(t)
	holdFree := func(port int) {
		if !portFree(port) {
			t.Skipf("port %d is not free on this host", port)
		}
	}
	holdFree(19101)
	holdFree(19102)
	m := newTestManager(t)

	de, us := Country{Region: "DE", Port: 19101}, Country{Region: "US", Port: 19102}
	if err := m.StartCountry(de); err != nil {
		t.Fatalf("StartCountry(DE): %v", err)
	}
	if err := m.StartCountry(us); err != nil {
		t.Fatalf("StartCountry(US): %v", err)
	}

	for _, c := range []Country{de, us} {
		st := m.CountryState(c.Region)
		if !st.Running || !listening(c.Port) {
			t.Fatalf("%s: state %+v, listening %v, want a process on port %d", c.Region, st, listening(c.Port), c.Port)
		}
		// The region reaches the process through its own config, which is what the fake reports back.
		eventually(t, c.Region+" tunnel", func() bool {
			s := m.CountryState(c.Region)
			return s.Tunnel.Connected && s.Tunnel.ServerRegion == c.Region
		})
	}
	if m.IsRunning() {
		t.Fatal("starting countries started the default instance")
	}

	m.StopAll()
	for _, c := range []Country{de, us} {
		eventually(t, c.Region+" port to close", func() bool { return !listening(c.Port) })
		if m.CountryState(c.Region).Running {
			t.Errorf("%s still running after StopAll", c.Region)
		}
	}
}

func TestStartCountryRefusesAPortSomethingHolds(t *testing.T) {
	useFakeCore(t)
	holdPort(t, 19101)
	m := newTestManager(t)

	err := m.StartCountry(Country{Region: "DE", Port: 19101})
	if err == nil || !strings.Contains(err.Error(), "held by another process") {
		t.Fatalf("StartCountry on a held port = %v, want the holder named as the reason", err)
	}
	if st := m.CountryState("DE"); st.Running || !strings.Contains(st.Error, "19101") {
		t.Fatalf("state %+v, want it down with the reason kept for the row", st)
	}
	// A start the operator was told failed is not retried behind their back.
	m.checkAll(time.Now().Add(time.Hour))
	if m.CountryState("DE").Running {
		t.Fatal("the supervisor started a country whose start had failed")
	}
}

func TestStartCountryRefusesAPairItDoesNotHandOut(t *testing.T) {
	useFakeCore(t)
	m := newTestManager(t)
	for _, c := range []Country{{Region: "../etc", Port: 19101}, {Region: "DE", Port: 80}} {
		if err := m.StartCountry(c); err == nil {
			t.Errorf("StartCountry(%+v) was accepted", c)
		}
	}
	if entries, _ := os.ReadDir(countriesDir()); len(entries) != 0 {
		t.Errorf("a refused country left files behind: %v", entries)
	}
}

func TestStopCountryKeepsItDownAndRemoveDeletesItsFiles(t *testing.T) {
	useFakeCore(t)
	if !portFree(19103) {
		t.Skip("port 19103 is not free on this host")
	}
	m := newTestManager(t)
	jp := Country{Region: "JP", Port: 19103}
	if err := m.StartCountry(jp); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}
	if err := m.StopCountry("JP"); err != nil {
		t.Fatalf("StopCountry: %v", err)
	}
	if m.CountryState("JP").Running || listening(jp.Port) {
		t.Fatal("the country is still up after StopCountry")
	}
	// Stopped by the operator: the supervisor leaves it alone, however long it looks.
	m.checkAll(time.Now().Add(time.Hour))
	if m.CountryState("JP").Running {
		t.Fatal("the supervisor started a country the operator stopped")
	}

	if err := m.RemoveCountry("JP"); err != nil {
		t.Fatalf("RemoveCountry: %v", err)
	}
	if _, err := os.Stat(countryDir("JP")); !os.IsNotExist(err) {
		t.Fatalf("the files of a removed country are still there (%v)", err)
	}
	if st := m.CountryState("JP"); st != (InstanceState{}) {
		t.Fatalf("a removed country has a state: %+v", st)
	}
	if err := m.RemoveCountry("../.."); err == nil {
		t.Fatal("RemoveCountry took a path for a region")
	}
	// Stopping one that never existed is not an error.
	if err := m.StopCountry("FR"); err != nil {
		t.Fatalf("StopCountry of an unknown region: %v", err)
	}
}

func TestSupervisorRestartsACountryThatCrashedAndWaitsLongerEachTime(t *testing.T) {
	useFakeCore(t)
	if !portFree(19104) {
		t.Skip("port 19104 is not free on this host")
	}
	shortenSupervision(t, 50*time.Millisecond, time.Hour) // every end counts as a failed start
	t.Setenv(fakeExitAfterEnv, "300ms")
	m := newTestManager(t)
	gb := Country{Region: "GB", Port: 19104}
	if err := m.StartCountry(gb); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}

	eventually(t, "the first crash", func() bool { return !m.CountryState("GB").Running })
	in := m.existingCountry("GB")
	now := time.Now()
	m.checkAll(now) // sees the end: waits restartBackoff before it tries
	if m.CountryState("GB").Running {
		t.Fatal("restarted before its back-off")
	}
	in.mu.Lock()
	first := in.life.retryAt.Sub(now)
	in.mu.Unlock()
	if first < 50*time.Millisecond || first > 80*time.Millisecond {
		t.Fatalf("first wait = %v, want about the 50ms back-off", first)
	}

	m.checkAll(now.Add(time.Second)) // past the back-off: starts it again
	if !m.CountryState("GB").Running || !listening(gb.Port) {
		t.Fatal("the supervisor did not bring the crashed country back")
	}

	eventually(t, "the second crash", func() bool { return !m.CountryState("GB").Running })
	now = time.Now()
	m.checkAll(now)
	in.mu.Lock()
	second := in.life.retryAt.Sub(now)
	in.mu.Unlock()
	if second < 2*first-20*time.Millisecond {
		t.Fatalf("second wait = %v, want it to double from %v: a process that cannot stay up must not be restarted at a steady rate", second, first)
	}
}

func TestSupervisorRestartsAtOnceATunnelThatRanLongEnough(t *testing.T) {
	useFakeCore(t)
	if !portFree(19105) {
		t.Skip("port 19105 is not free on this host")
	}
	shortenSupervision(t, time.Hour, time.Nanosecond) // any end counts as a crash of a stable process
	t.Setenv(fakeExitAfterEnv, "300ms")
	m := newTestManager(t)
	if err := m.StartCountry(Country{Region: "SE", Port: 19105}); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}
	eventually(t, "the crash", func() bool { return !m.CountryState("SE").Running })

	m.checkAll(time.Now())
	if !m.CountryState("SE").Running {
		t.Fatal("a tunnel that had been up was not restarted at once")
	}
}

func TestSupervisorKeepsTheDefaultInstanceUpToo(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("the default port is not free on this host")
	}
	shortenSupervision(t, time.Hour, time.Nanosecond)
	t.Setenv(fakeExitAfterEnv, "300ms")
	m := newTestManager(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "the crash", func() bool { return !m.IsRunning() })
	m.checkAll(time.Now())
	if !m.IsRunning() {
		t.Fatal("the default instance was not brought back")
	}
	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	m.checkAll(time.Now().Add(time.Hour))
	if m.IsRunning() {
		t.Fatal("the supervisor restarted a default instance that was stopped on purpose")
	}
}

func TestStartFailureIsKeptForTheStatusAndNotRetriedByTheSupervisor(t *testing.T) {
	useFakeCore(t)
	if !portFree(19106) {
		t.Skip("port 19106 is not free on this host")
	}
	t.Setenv(fakeFailStartEnv, "1")
	m := newTestManager(t)
	err := m.StartCountry(Country{Region: "NL", Port: 19106})
	if err == nil || !strings.Contains(err.Error(), "exited during startup") {
		t.Fatalf("StartCountry of a process that cannot start = %v", err)
	}
	st := m.CountryState("NL")
	if st.Running || st.Error == "" {
		t.Fatalf("state %+v, want it down with the error kept", st)
	}
	m.checkAll(time.Now().Add(time.Hour))
	if m.CountryState("NL").Running {
		t.Fatal("the supervisor started a country the operator saw fail")
	}
}

func TestABootStartThatFailedIsRetriedByTheSupervisor(t *testing.T) {
	cases := map[string]struct {
		port    int
		start   func(*Manager) error
		running func(*Manager) bool
	}{
		"default": {
			SocksPort,
			func(m *Manager) error { return m.StartSupervised() },
			func(m *Manager) bool { return m.IsRunning() },
		},
		"country": {
			19110,
			func(m *Manager) error { return m.StartCountrySupervised(Country{Region: "DE", Port: 19110}) },
			func(m *Manager) bool { return m.CountryState("DE").Running },
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			useFakeCore(t)
			shortenSupervision(t, time.Hour, time.Hour)
			holder, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tc.port)))
			if err != nil {
				t.Skipf("port %d is not free on this host: %v", tc.port, err)
			}
			defer holder.Close()
			m := newTestManager(t)

			if err := tc.start(m); err == nil {
				t.Fatal("the start on a held port was reported as a success")
			}
			if err := holder.Close(); err != nil {
				t.Fatal(err)
			}
			m.checkAll(time.Now())
			if tc.running(m) {
				t.Fatal("the start was retried before its back-off was over")
			}
			m.checkAll(time.Now().Add(2 * time.Hour))
			if !tc.running(m) {
				t.Fatal("a boot start that failed was never retried: the tunnel stays down although it should run")
			}
		})
	}
}

func TestASupervisorLookThatPanicsDoesNotEndThePanel(t *testing.T) {
	useFakeCore(t)
	m := newTestManager(t)
	in := &instance{tag: "[XX]", spec: func() (spec, error) { panic("a bug in a look") }}
	in.life.wanted = true
	m.mu.Lock()
	m.countries["XX"] = &countrySlot{country: Country{Region: "XX", Port: 19111}, inst: in}
	m.mu.Unlock()

	// Twice: a look that left its lock held would stop the second one.
	m.checkAll(time.Now())
	m.checkAll(time.Now())
}

func TestAStopByTheOperatorEndsTheStoryOfAFailedStart(t *testing.T) {
	useFakeCore(t)
	holdPort(t, 19112)
	m := newTestManager(t)
	if err := m.StartCountrySupervised(Country{Region: "DE", Port: 19112}); err == nil {
		t.Fatal("the start on a held port was reported as a success")
	}
	if m.CountryState("DE").Error == "" {
		t.Fatal("the failed start left no reason for the row to show")
	}

	if err := m.StopCountry("DE"); err != nil {
		t.Fatal(err)
	}
	if st := m.CountryState("DE"); st.Error != "" {
		t.Fatalf("state %+v after the stop, want the old failure forgotten: a stopped country is not a failed one", st)
	}
	m.checkAll(time.Now().Add(time.Hour))
	if m.CountryState("DE").Running {
		t.Fatal("the supervisor started a country that was stopped on purpose")
	}
}

func TestASupervisorLookThatPanicsBeforeItStartsAnythingLeavesNothingLocked(t *testing.T) {
	useFakeCore(t)
	m := newTestManager(t)
	// A process with no log writer: reading how it ended panics, with the lock of the instance taken.
	in := &instance{tag: "[XX]", spec: func() (spec, error) { return spec{}, nil }, proc: &Process{}}
	in.life.wanted = true
	m.mu.Lock()
	m.countries["XX"] = &countrySlot{country: Country{Region: "XX", Port: 19113}, inst: in}
	m.mu.Unlock()

	m.checkAll(time.Now())
	m.checkAll(time.Now())

	free := make(chan struct{})
	go func() {
		in.isRunning()
		close(free)
	}()
	select {
	case <-free:
	case <-time.After(5 * time.Second):
		t.Fatal("a look that panicked left the lock of the instance held")
	}
}

func TestForgettingTheCountriesStopsTheirProcesses(t *testing.T) {
	useFakeCore(t)
	holdFree := func(port int) {
		if !portFree(port) {
			t.Skipf("port %d is not free on this host", port)
		}
	}
	holdFree(19114)
	m := newTestManager(t)
	if err := m.StartCountry(Country{Region: "DE", Port: 19114}); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}
	in := m.existingCountry("DE")
	t.Cleanup(func() { _ = in.stop() })

	m.forgetCountries()

	eventually(t, "the port to close", func() bool { return !listening(19114) })
	if m.existingCountry("DE") != nil {
		t.Fatal("the country is still known to the manager")
	}
}

func TestAStopByTheOperatorStartsTheBackOffOver(t *testing.T) {
	useFakeCore(t)
	shortenSupervision(t, time.Hour, time.Hour)
	holdPort(t, 19115)
	m := newTestManager(t)
	c := Country{Region: "DE", Port: 19115}
	if err := m.StartCountrySupervised(c); err == nil {
		t.Fatal("the start on a held port was reported as a success")
	}
	m.checkAll(time.Now().Add(2 * time.Hour)) // the retry fails as well, so the wait doubles
	in := m.existingCountry("DE")
	in.mu.Lock()
	grown := in.life.delay
	in.mu.Unlock()
	if grown <= time.Hour {
		t.Fatalf("setup: the wait is %v after two failures, want it grown", grown)
	}

	if err := m.StopCountry("DE"); err != nil {
		t.Fatal(err)
	}
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.life.delay != 0 || !in.life.retryAt.IsZero() {
		t.Fatalf("after a stop the wait is %v and the next try %v, want a clean slate for the next start", in.life.delay, in.life.retryAt)
	}
}

func TestRestartCountriesGivesThemTheNewBaseConfig(t *testing.T) {
	useFakeCore(t)
	if !portFree(19107) {
		t.Skip("port 19107 is not free on this host")
	}
	m := newTestManager(t)
	it := Country{Region: "IT", Port: 19107}
	if err := m.StartCountry(it); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}
	if err := SaveConfig([]byte(`{"PropagationChannelId":"replaced","SponsorId":"test"}`)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := m.RestartCountries(); err != nil {
		t.Fatalf("RestartCountries: %v", err)
	}
	sp, _ := countrySpec(it)
	raw, err := os.ReadFile(sp.config)
	if err != nil || !strings.Contains(string(raw), `"replaced"`) {
		t.Fatalf("the country config after a restart (%v): %s", err, raw)
	}
	if !m.CountryState("IT").Running || !listening(it.Port) {
		t.Fatal("the country is not running after RestartCountries")
	}
	// The default instance was not asked to restart.
	if m.IsRunning() {
		t.Fatal("RestartCountries started the default instance")
	}
}

func TestACountryCannotBeMovedToAnotherPortWhileItRuns(t *testing.T) {
	useFakeCore(t)
	if !portFree(19108) || !portFree(19109) {
		t.Skip("ports 19108-19109 are not free on this host")
	}
	m := newTestManager(t)
	if err := m.StartCountry(Country{Region: "ES", Port: 19108}); err != nil {
		t.Fatalf("StartCountry: %v", err)
	}
	if err := m.StartCountry(Country{Region: "ES", Port: 19109}); err == nil {
		t.Fatal("a running country was given another port from under its process")
	}
	if !listening(19108) {
		t.Fatal("the refused move disturbed the running country")
	}
	// Once stopped, the same region may take another port.
	if err := m.StopCountry("ES"); err != nil {
		t.Fatal(err)
	}
	if err := m.StartCountry(Country{Region: "ES", Port: 19109}); err != nil {
		t.Fatalf("StartCountry on the new port: %v", err)
	}
	if !listening(19109) {
		t.Fatal("the country is not on its new port")
	}
}

// startedAt is when the process of in came up, as the manager wrote it down at the start.
func startedAt(in *instance) time.Time {
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.life.startedAt
}

func TestRestartRunningRestartsWhatRunsAndLeavesWhatIsStopped(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) || !portFree(19110) || !portFree(19111) {
		t.Skip("ports 19060 and 19110-19111 are not free on this host")
	}
	m := newTestManager(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.StartCountry(Country{Region: "IT", Port: 19110}); err != nil {
		t.Fatalf("StartCountry IT: %v", err)
	}
	if err := m.StartCountry(Country{Region: "ES", Port: 19111}); err != nil {
		t.Fatalf("StartCountry ES: %v", err)
	}
	if err := m.StopCountry("ES"); err != nil {
		t.Fatalf("StopCountry ES: %v", err)
	}
	defBefore, itBefore := startedAt(m.def), startedAt(m.existingCountry("IT"))
	time.Sleep(20 * time.Millisecond)

	if err := m.restartRunning(); err != nil {
		t.Fatalf("restartRunning: %v", err)
	}

	if !startedAt(m.def).After(defBefore) || !m.IsRunning() || !listening(SocksPort) {
		t.Error("the default process was not restarted")
	}
	if !startedAt(m.existingCountry("IT")).After(itBefore) || !m.CountryState("IT").Running || !listening(19110) {
		t.Error("the running country was not restarted")
	}
	if m.CountryState("ES").Running || listening(19111) {
		t.Error("restartRunning started a country that had been stopped")
	}
}

func TestRestartRunningDoesNotStartTheDefaultProcessThatIsStopped(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)

	if err := m.restartRunning(); err != nil {
		t.Fatalf("restartRunning: %v", err)
	}

	if m.IsRunning() || listening(SocksPort) {
		t.Fatal("restartRunning started the default process that was stopped")
	}
}

func TestAfterUpgradeRestartsTheTunnelsOnlyWhenTheFileWasReplaced(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := startedAt(m.def)
	time.Sleep(20 * time.Millisecond)

	if replaced, err := m.afterUpgrade(false, nil); replaced || err != nil {
		t.Fatalf("afterUpgrade(false, nil) = %v, %v, want nothing", replaced, err)
	}
	if replaced, err := m.afterUpgrade(false, errors.New("no network")); replaced || err == nil || err.Error() != "no network" {
		t.Fatalf("afterUpgrade(false, error) = %v, %v, want the error passed on", replaced, err)
	}
	if !startedAt(m.def).Equal(before) {
		t.Fatal("a build that was not replaced restarted the tunnel")
	}

	if replaced, err := m.afterUpgrade(true, nil); !replaced || err != nil {
		t.Fatalf("afterUpgrade(true, nil) = %v, %v, want the replacement reported", replaced, err)
	}
	if !startedAt(m.def).After(before) || !m.IsRunning() {
		t.Fatal("a replaced build did not restart the tunnel that ran from the old file")
	}
}

func TestRestartRunningRestartsATunnelThatIsStillStarting(t *testing.T) {
	useFakeCore(t)
	if !portFree(19112) {
		t.Skip("port 19112 is not free on this host")
	}
	m := newTestManager(t)
	nl := Country{Region: "NL", Port: 19112}
	if err := m.PrepareCountry(nl); err != nil {
		t.Fatalf("PrepareCountry: %v", err)
	}
	in := m.existingCountry("NL")
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	render := in.prepare
	in.prepare = func() error {
		once.Do(func() { close(entered) })
		<-release
		return render()
	}
	started := make(chan error, 1)
	go func() { started <- m.StartCountrySupervised(nl) }()
	<-entered // the start is under way: the tunnel is meant to run and has no process yet

	beforeRestart := time.Now()
	restarted := make(chan error, 1)
	go func() { restarted <- m.restartRunning() }()
	close(release)

	if err := <-started; err != nil {
		t.Fatalf("StartCountrySupervised: %v", err)
	}
	if err := <-restarted; err != nil {
		t.Fatalf("restartRunning: %v", err)
	}
	if startedAt(in).Before(beforeRestart) {
		t.Fatal("a tunnel that was still starting was skipped, so it runs from the file it began with")
	}
	if !m.CountryState("NL").Running || !listening(19112) {
		t.Fatal("the country is not running after the restart")
	}
}

// A restart that arrives while a stop holds the lifecycle lock decides once the stop is over, and does not undo it.
func TestRestartRunningDoesNotUndoAStopThatLandsFirst(t *testing.T) {
	for name, restart := range map[string]func(m *Manager) error{
		"the upgrade":   (*Manager).restartRunning,
		"a config save": (*Manager).RestartCountries,
	} {
		t.Run(name, func(t *testing.T) {
			useFakeCore(t)
			if !portFree(19113) {
				t.Skip("port 19113 is not free on this host")
			}
			m := newTestManager(t)
			if err := m.StartCountry(Country{Region: "IT", Port: 19113}); err != nil {
				t.Fatalf("StartCountry: %v", err)
			}
			in := m.existingCountry("IT")

			in.lifecycle.Lock() // the stop is under way
			restarted := make(chan error, 1)
			go func() { restarted <- restart(m) }()
			time.Sleep(50 * time.Millisecond) // long enough for a restart that decides before the lock to have decided
			stopErr := in.stopLocked()
			in.lifecycle.Unlock()

			if stopErr != nil {
				t.Fatalf("stopLocked: %v", stopErr)
			}
			if err := <-restarted; err != nil {
				t.Fatalf("restart: %v", err)
			}
			if m.CountryState("IT").Running || listening(19113) {
				t.Fatal("the restart started again a tunnel that had been stopped")
			}
		})
	}
}

func TestRestartRunningStartsADefaultProcessThatShouldRunButIsDown(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)
	t.Setenv(fakeFailStartEnv, "1")
	if err := m.StartSupervised(); err == nil {
		t.Fatal("setup: a process that ends at once was reported as started")
	}
	t.Setenv(fakeFailStartEnv, "")
	if m.IsRunning() {
		t.Fatal("setup: the default process runs")
	}

	if err := m.restartRunning(); err != nil {
		t.Fatalf("restartRunning: %v", err)
	}

	if !m.IsRunning() || !listening(SocksPort) {
		t.Fatal("a default process that is meant to run, and was down, was not brought up by the restart")
	}
}

// The upgrade starts a country that should run and is down, from the new file; the supervisor would do it later.
func TestRestartRunningStartsACountryThatShouldRunButIsDown(t *testing.T) {
	useFakeCore(t)
	if !portFree(19117) {
		t.Skip("port 19117 is not free on this host")
	}
	m := newTestManager(t)
	fr := Country{Region: "FR", Port: 19117}
	t.Setenv(fakeFailStartEnv, "1")
	if err := m.StartCountrySupervised(fr); err == nil {
		t.Fatal("setup: a process that ends at once was reported as started")
	}
	t.Setenv(fakeFailStartEnv, "")

	if err := m.restartRunning(); err != nil {
		t.Fatalf("restartRunning: %v", err)
	}

	if !m.CountryState("FR").Running || !listening(fr.Port) {
		t.Error("a country that is meant to run, and was down, was not brought up by the restart")
	}
}

// A config save reaches the countries that run; one that is down is the supervisor's to start, and its failure is no
// reason to answer a saved config with an error.
func TestRestartCountriesLeavesACountryThatIsDownToTheSupervisor(t *testing.T) {
	useFakeCore(t)
	if !portFree(19116) {
		t.Skip("port 19116 is not free on this host")
	}
	m := newTestManager(t)
	de := Country{Region: "DE", Port: 19116}
	t.Setenv(fakeFailStartEnv, "1")
	if err := m.StartCountrySupervised(de); err == nil {
		t.Fatal("setup: a process that ends at once was reported as started")
	}
	t.Setenv(fakeFailStartEnv, "")

	if err := m.RestartCountries(); err != nil {
		t.Fatalf("RestartCountries: %v", err)
	}

	if m.CountryState("DE").Running || listening(de.Port) {
		t.Error("a config save started a country that was down, which is the supervisor's to start")
	}
}

// A config save that found the default process up decides again under the lifecycle lock, so a stop that lands first
// is not undone.
func TestRestartIfRunningDoesNotUndoAStopThatLandsFirst(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	m.def.lifecycle.Lock() // the stop is under way
	restarted := make(chan error, 1)
	go func() { restarted <- m.RestartIfRunning() }()
	time.Sleep(50 * time.Millisecond) // long enough for a restart that decides before the lock to have decided
	stopErr := m.def.stopLocked()
	m.def.lifecycle.Unlock()

	if stopErr != nil {
		t.Fatalf("stopLocked: %v", stopErr)
	}
	if err := <-restarted; err != nil {
		t.Fatalf("RestartIfRunning: %v", err)
	}
	if m.IsRunning() || listening(SocksPort) {
		t.Fatal("the restart started again the default process that had been stopped")
	}
}

// A config save leaves a default process that is down to the supervisor, and its failure to start is not the save's.
func TestRestartIfRunningLeavesTheDefaultProcessThatIsDownToTheSupervisor(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)
	t.Setenv(fakeFailStartEnv, "1")
	if err := m.StartSupervised(); err == nil {
		t.Fatal("setup: a process that ends at once was reported as started")
	}
	t.Setenv(fakeFailStartEnv, "")

	if err := m.RestartIfRunning(); err != nil {
		t.Fatalf("RestartIfRunning: %v", err)
	}

	if m.IsRunning() || listening(SocksPort) {
		t.Error("a config save started the default process that was down, which is the supervisor's to start")
	}
}

// A config save restarts the default process that is up, which is how the new config reaches it.
func TestRestartIfRunningRestartsTheDefaultProcessThatIsUp(t *testing.T) {
	useFakeCore(t)
	if !portFree(SocksPort) {
		t.Skip("port 19060 is not free on this host")
	}
	m := newTestManager(t)
	if err := m.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	before := startedAt(m.def)
	time.Sleep(20 * time.Millisecond)

	if err := m.RestartIfRunning(); err != nil {
		t.Fatalf("RestartIfRunning: %v", err)
	}

	if !startedAt(m.def).After(before) || !m.IsRunning() || !listening(SocksPort) {
		t.Error("the default process that was up was not restarted")
	}
}
