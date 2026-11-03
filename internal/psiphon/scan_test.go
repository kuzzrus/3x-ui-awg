//go:build !windows

package psiphon

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// shortScan paces a scan for a test: a region gets connect to come up, and the scan looks at it often.
func shortScan(t *testing.T, connect time.Duration) {
	t.Helper()
	oldConnect, oldDiscover, oldPoll := scanConnectTimeout, scanDiscoverTimeout, scanPoll
	scanConnectTimeout, scanDiscoverTimeout, scanPoll = connect, connect, 20*time.Millisecond
	t.Cleanup(func() { scanConnectTimeout, scanDiscoverTimeout, scanPoll = oldConnect, oldDiscover, oldPoll })
}

// newTestScanner is a scanner of its own, ended with the test.
func newTestScanner(t *testing.T) *Scanner {
	t.Helper()
	sc := newScanner()
	t.Cleanup(sc.Cancel)
	return sc
}

// scanProcesses counts the scan ports something listens on: the temporary tunnels that are up.
func scanProcesses() int {
	n := 0
	for port := scanPortFirst; port <= scanPortLast; port++ {
		if listening(port) {
			n++
		}
	}
	return n
}

// beginScan starts a scan and returns the channel its final status arrives on.
func beginScan(t *testing.T, sc *Scanner, regions []string) <-chan ScanStatus {
	t.Helper()
	done := make(chan ScanStatus, 1)
	if err := sc.Start(regions, func(st ScanStatus) { done <- st }); err != nil {
		t.Fatalf("Start(%v): %v", regions, err)
	}
	return done
}

// scanAndWait runs a scan to its end and returns the final status, its results in the order of their regions.
func scanAndWait(t *testing.T, sc *Scanner, regions []string) ScanStatus {
	t.Helper()
	select {
	case st := <-beginScan(t, sc, regions):
		slices.SortFunc(st.Results, func(a, b ScanResult) int { return strings.Compare(a.Region, b.Region) })
		return st
	case <-time.After(30 * time.Second):
		t.Fatal("the scan did not end")
		return ScanStatus{}
	}
}

// scanThroughSocks makes every tunnel the fake starts carry a probe, and points the probe at servers of the test.
func scanThroughSocks(t *testing.T) {
	t.Helper()
	t.Setenv(fakeSocksEnv, "1")
	ok, exit := probeServers(t)
	withProbeTargets(t, []string{ok.URL}, exit.URL)
}

func TestScanConnectsEachRegionAndMeasuresIt(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)

	got := scanAndWait(t, newTestScanner(t), []string{"DE", "US", "JP"})

	if got.Running || got.Cancelled || got.Error != "" || got.Total != 3 || len(got.Results) != 3 {
		t.Fatalf("status = %+v, want a finished scan of three regions", got)
	}
	for i, region := range []string{"DE", "JP", "US"} {
		r := got.Results[i]
		if r.Region != region || !r.Connected || r.Error != "" {
			t.Fatalf("result %d = %+v, want %s connected with no error", i, r, region)
		}
		if r.IP != "203.0.113.9" || r.Country != "DE" {
			t.Errorf("%s: exit %s in %s, want what the probe found", region, r.IP, r.Country)
		}
		if r.LatencyMs < probeLatency.Milliseconds() || r.LatencyMs > 5000 {
			t.Errorf("%s: latency %d ms, want about the %v the probe address took", region, r.LatencyMs, probeLatency)
		}
		if r.ConnectMs <= 0 {
			t.Errorf("%s: connect time %d ms, want how long the tunnel took", region, r.ConnectMs)
		}
	}
	if got.StartedAt == 0 || got.FinishedAt < got.StartedAt || len(got.Current) != 0 {
		t.Errorf("times %d..%d and current %v, want a scan that ran and has nothing left in progress", got.StartedAt, got.FinishedAt, got.Current)
	}
	if entries, _ := os.ReadDir(scanDir()); len(entries) != 0 {
		t.Errorf("the scan left files behind: %v", entries)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up after the scan", n)
	}
}

func TestScanTakesEachRegionOnce(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)

	got := scanAndWait(t, newTestScanner(t), []string{"DE", "DE", "US"})

	if got.Total != 2 || len(got.Results) != 2 {
		t.Fatalf("status = %+v, want DE and US scanned once each", got)
	}
}

func TestScanSaysSoWhenARegionNeverConnects(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "US")
	shortScan(t, 600*time.Millisecond)

	got := scanAndWait(t, newTestScanner(t), []string{"DE", "US"})

	de, us := got.Results[0], got.Results[1]
	if !de.Connected || de.Error != "" {
		t.Fatalf("DE = %+v, want it connected, a region that does not is no reason to hold up the others", de)
	}
	if us.Connected || us.LatencyMs != 0 || !strings.Contains(us.Error, "did not connect") {
		t.Fatalf("US = %+v, want a region that never got a tunnel reported so, with no measurement", us)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up, want the one that did not connect taken down", n)
	}
}

func TestScanReportsATunnelThatCannotStartAndGoesOn(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeFailRegionsEnv, "JP")

	got := scanAndWait(t, newTestScanner(t), []string{"DE", "JP"})

	de, jp := got.Results[0], got.Results[1]
	if !de.Connected {
		t.Fatalf("DE = %+v, want it scanned although JP failed", de)
	}
	if jp.Connected || !strings.Contains(jp.Error, "refusing to start in JP") {
		t.Fatalf("JP = %+v, want what the process said as the reason", jp)
	}
}

func TestScanCancelStopsEveryProcessAndRemovesItsFiles(t *testing.T) {
	useFakeCore(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE,US,JP")
	shortScan(t, time.Minute)
	sc := newTestScanner(t)
	done := beginScan(t, sc, []string{"DE", "US", "JP"})
	eventually(t, "three temporary tunnels", func() bool { return scanProcesses() == 3 })

	began := time.Now()
	sc.Cancel()
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("Cancel took %v, want it to end the tunnels at once instead of waiting for them to connect", took)
	}

	got := sc.Status()
	if got.Running || !got.Cancelled || len(got.Results) != 0 || len(got.Current) != 0 {
		t.Fatalf("status = %+v, want a cancelled scan with nothing measured and nothing in progress", got)
	}
	if final := <-done; !final.Cancelled {
		t.Errorf("the final status handed to the caller = %+v, want it marked cancelled", final)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up after Cancel", n)
	}
	if entries, _ := os.ReadDir(scanDir()); len(entries) != 0 {
		t.Errorf("Cancel left files behind: %v", entries)
	}
}

func TestScanRunsAFewRegionsAtOnceAndNoMore(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeConnectAfterEnv, "300ms")
	oldParallel := scanParallel
	scanParallel = 2
	t.Cleanup(func() { scanParallel = oldParallel })
	sc := newTestScanner(t)
	done := beginScan(t, sc, []string{"DE", "US", "JP", "FR", "NL", "SE"})

	peak := 0
	for ended := false; !ended; {
		select {
		case <-done:
			ended = true
		default:
			peak = max(peak, len(sc.Status().Current))
			time.Sleep(5 * time.Millisecond)
		}
	}
	if peak != 2 {
		t.Fatalf("at most %d regions were scanned at once, want exactly the %d it is allowed", peak, scanParallel)
	}
	got := sc.Status()
	if len(got.Results) != 6 {
		t.Fatalf("%d results, want all six regions scanned", len(got.Results))
	}
	// Two tunnels given the same port would not both come up, so each of them having connected says they had ports of their own.
	for _, r := range got.Results {
		if !r.Connected || r.Error != "" {
			t.Errorf("%+v, want every region connected on a port of its own", r)
		}
	}
}

func TestScanSaysSoWhenATunnelDiesBeforeItConnects(t *testing.T) {
	useFakeCore(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE")
	t.Setenv(fakeExitAfterEnv, "800ms")
	shortScan(t, time.Minute)

	began := time.Now()
	got := scanAndWait(t, newTestScanner(t), []string{"DE"})

	if r := got.Results[0]; r.Connected || !strings.Contains(r.Error, "exited before it connected") || !strings.Contains(r.Error, "crashed") {
		t.Fatalf("DE = %+v, want the crash named as the reason", r)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Fatalf("the scan took %v, want it to notice the end of the process and not wait out the minute it was given", took)
	}
}

func TestScanRefusesASecondScanWhileOneRuns(t *testing.T) {
	useFakeCore(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE")
	shortScan(t, time.Minute)
	sc := newTestScanner(t)
	beginScan(t, sc, []string{"DE"})

	err := sc.Start([]string{"US"}, nil)

	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second Start = %v, want it refused for the scan that is running", err)
	}
}

func TestScanWithoutRegionsLearnsThemFromATunnel(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeRegionsEnv, "DE,US")

	got := scanAndWait(t, newTestScanner(t), nil)

	if got.Error != "" || got.Discovering || got.Total != 2 || len(got.Results) != 2 {
		t.Fatalf("status = %+v, want the two regions the tunnel named scanned", got)
	}
	if got.Results[0].Region != "DE" || got.Results[1].Region != "US" {
		t.Fatalf("results %+v, want DE and US and not the tunnel that asked", got.Results)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up, want the one that asked taken down too", n)
	}
}

func TestScanSaysSoWhenNoTunnelTellsTheRegions(t *testing.T) {
	useFakeCore(t)
	shortScan(t, 500*time.Millisecond)

	got := scanAndWait(t, newTestScanner(t), nil)

	if !strings.Contains(got.Error, "which regions") || got.Total != 0 || len(got.Results) != 0 || got.Running {
		t.Fatalf("status = %+v, want a finished scan that says it could not learn the regions", got)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up after the scan gave up", n)
	}
}

func TestScanCancelledWhileItAsksForTheRegionsIsNotAnError(t *testing.T) {
	useFakeCore(t)
	shortScan(t, time.Minute)
	sc := newTestScanner(t)
	done := beginScan(t, sc, nil)
	eventually(t, "the temporary tunnel that asks", func() bool { return scanProcesses() == 1 })

	sc.Cancel()

	got := <-done
	if !got.Cancelled || got.Error != "" || got.Running || len(got.Results) != 0 {
		t.Fatalf("status = %+v, want a scan that says it was cancelled and not that it could not learn the regions", got)
	}
}

func TestScanRefusesWhatCannotRun(t *testing.T) {
	useFakeCore(t)
	cases := []struct {
		name    string
		prepare func(t *testing.T)
		regions []string
		want    string
	}{
		{"no binary", func(t *testing.T) { t.Setenv("XUI_BIN_FOLDER", t.TempDir()) }, []string{"DE"}, "not installed"},
		{"no config", func(t *testing.T) {
			if err := os.Remove(ConfigPath()); err != nil {
				t.Fatal(err)
			}
		}, []string{"DE"}, "no config"},
		{"a path for a region", func(*testing.T) {}, []string{"DE", "../x"}, `"../x"`},
		{"a region in lower case", func(*testing.T) {}, []string{"de"}, `"de"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFakeCore(t)
			tc.prepare(t)
			sc := newTestScanner(t)

			err := sc.Start(tc.regions, nil)

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Start(%v) = %v, want a refusal naming %s", tc.regions, err, tc.want)
			}
			if st := sc.Status(); st.Running || st.StartedAt != 0 {
				t.Fatalf("a refused scan left a status: %+v", st)
			}
			if _, err := os.Stat(scanDir()); err == nil {
				t.Fatal("a refused scan made its folder")
			}
		})
	}
}

func TestScanIgnoresTheNoticesAnEarlierCrashedScanLeft(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE")
	shortScan(t, 600*time.Millisecond)
	leftover := scanSpec("DE", scanPortFirst)
	if err := os.MkdirAll(filepath.Dir(leftover.notices), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := `{"noticeType":"Tunnels","data":{"count":1}}` + "\n"
	if err := os.WriteFile(leftover.notices, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scanAndWait(t, newTestScanner(t), []string{"DE"})

	if r := got.Results[0]; r.Connected || !strings.Contains(r.Error, "did not connect") {
		t.Fatalf("DE = %+v, want the tunnel that never came up reported so, not read as connected from the notice an earlier scan left", r)
	}
}

func TestScanDiscoveryIgnoresTheListAnEarlierCrashedScanLeft(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	t.Setenv(fakeRegionsEnv, "DE,US")
	t.Setenv(fakeRegionsAfterEnv, "300ms")
	leftover := scanSpec("discover", scanPortFirst)
	if err := os.MkdirAll(filepath.Dir(leftover.notices), 0o700); err != nil {
		t.Fatal(err)
	}
	stale := `{"noticeType":"AvailableEgressRegions","data":{"regions":["FR"]}}` + "\n"
	if err := os.WriteFile(leftover.notices, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	got := scanAndWait(t, newTestScanner(t), nil)

	if got.Total != 2 || len(got.Results) != 2 || got.Results[0].Region != "DE" || got.Results[1].Region != "US" {
		t.Fatalf("status = %+v, want the regions the live tunnel named (DE, US) and not the ones an earlier scan left (FR)", got)
	}
}

func TestScanReadsAsFinishedOnlyAfterItsResultIsStored(t *testing.T) {
	useFakeCore(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE")
	shortScan(t, 300*time.Millisecond)
	sc := newTestScanner(t)
	storing, stored := make(chan ScanStatus, 1), make(chan struct{})
	if err := sc.Start([]string{"DE"}, func(ScanStatus) {
		storing <- sc.Status()
		<-stored
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	var during ScanStatus
	select {
	case during = <-storing:
	case <-time.After(30 * time.Second):
		t.Fatal("the scan did not hand over its result")
	}
	close(stored)

	if !during.Running || during.FinishedAt != 0 {
		t.Fatalf("status = %+v while the result was still being stored, want a scan that has not ended: a page that reads the regions when it sees it end gets the ones from before the scan", during)
	}
	eventually(t, "the scan to read as finished once its result is stored", func() bool {
		st := sc.Status()
		return !st.Running && st.FinishedAt != 0 && len(st.Results) == 1
	})
}

func TestScanTunnelNeverClearsAFolderThatIsNotTheScans(t *testing.T) {
	useFakeCore(t)

	proc, err := startScanProcess(context.Background(), defaultSpec(), "")

	if err == nil {
		_ = proc.Stop()
		t.Fatal("startScanProcess started a tunnel in the folder of the default one, want it refused")
	}
	if !IsConfigured() || !IsInstalled() {
		t.Fatalf("the folder of the default tunnel was cleared (config there: %v, binary there: %v), want it left alone", IsConfigured(), IsInstalled())
	}
}

func TestScanSkipsAPortSomethingHolds(t *testing.T) {
	useFakeCore(t)
	scanThroughSocks(t)
	holdPort(t, scanPortFirst)

	got := scanAndWait(t, newTestScanner(t), []string{"DE"})

	if r := got.Results[0]; !r.Connected || r.Error != "" {
		t.Fatalf("DE = %+v, want it scanned on a port nothing held", r)
	}
}

func TestStopAllEndsARunningScan(t *testing.T) {
	useFakeCore(t)
	t.Setenv(fakeNoTunnelRegionsEnv, "DE")
	shortScan(t, time.Minute)
	m := newTestManager(t)
	if err := m.scanner.Start([]string{"DE"}, nil); err != nil {
		t.Fatalf("Start: %v", err)
	}
	eventually(t, "the temporary tunnel", func() bool { return scanProcesses() == 1 })

	m.StopAll()

	if st := m.scanner.Status(); st.Running || !st.Cancelled {
		t.Fatalf("status = %+v, want the scan ended by the shutdown", st)
	}
	if n := scanProcesses(); n != 0 {
		t.Errorf("%d temporary tunnels are still up after StopAll", n)
	}
}
