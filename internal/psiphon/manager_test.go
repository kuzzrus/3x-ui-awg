//go:build !windows

package psiphon

import (
	"net"
	"os"
	"strconv"
	"strings"
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
