package integration

import (
	"slices"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// answeringScans stands in for the scanner for the length of a test: it writes down what it was asked to scan and
// the function that wants the final status, and answers with the status it is given.
type answeringScans struct {
	mu       sync.Mutex
	asked    [][]string
	onFinish func(psiphon.ScanStatus)
	live     psiphon.ScanStatus
}

func useScans(t *testing.T) *answeringScans {
	t.Helper()
	useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	fake := &answeringScans{}
	previous := scans
	t.Cleanup(func() { scans = previous })
	scans = scanOps{
		start: func(regions []string, onFinish func(psiphon.ScanStatus)) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.asked = append(fake.asked, regions)
			fake.onFinish = onFinish
			return nil
		},
		cancel: func() {},
		status: func() psiphon.ScanStatus {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return fake.live
		},
	}
	return fake
}

func TestScanOfNoRegionsTakesTheOnesATunnelSaidHaveServers(t *testing.T) {
	fake := useScans(t)
	tunnelSaysRegions(t, "2026-10-27T09:41:45.362Z", "US", "DE")

	if err := (&PsiphonService{}).ScanRegions(nil); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}

	if len(fake.asked) != 1 || !slices.Equal(fake.asked[0], []string{"DE", "US"}) {
		t.Fatalf("the scanner was asked for %v, want the regions of the tunnel's list", fake.asked)
	}
}

func TestScanOfNoRegionsLeavesTheListToTheScannerWhenNoTunnelHasSaidIt(t *testing.T) {
	fake := useScans(t)

	if err := (&PsiphonService{}).ScanRegions(nil); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}

	if len(fake.asked) != 1 || len(fake.asked[0]) != 0 {
		t.Fatalf("the scanner was asked for %v, want no regions, which is how it learns them itself", fake.asked)
	}
}

func TestScanOfNamedRegionsTakesThemAsGiven(t *testing.T) {
	fake := useScans(t)
	tunnelSaysRegions(t, "2026-10-27T09:41:45.362Z", "DE", "US")

	if err := (&PsiphonService{}).ScanRegions([]string{"FR", "JP"}); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}

	if len(fake.asked) != 1 || !slices.Equal(fake.asked[0], []string{"FR", "JP"}) {
		t.Fatalf("the scanner was asked for %v, want the two regions that were named, listed or not", fake.asked)
	}
}

func TestAScanThatHadToAskWhichRegionsHaveServersKeepsTheListAndItsOwnResult(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.ScanRegions(nil); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}

	fake.onFinish(psiphon.ScanStatus{
		Total: 2, StartedAt: 1, FinishedAt: 2,
		Results: []psiphon.ScanResult{
			{Region: "US", Connected: true, LatencyMs: 62},
			{Region: "DE", Error: "did not connect within 1m0s"},
		},
	})

	got := availability(svc.Regions())
	if got["DE"] != "yes" || got["US"] != "yes" || got["FR"] != "no" {
		t.Fatalf("after a scan of DE and US that asked for the list: DE %s, US %s, FR %s, want the regions it scanned marked as having servers and the others as having none", got["DE"], got["US"], got["FR"])
	}
	fake.live = psiphon.ScanStatus{}
	if kept := svc.ScanStatus(); kept.FinishedAt != 2 || len(kept.Results) != 2 {
		t.Fatalf("status = %+v, want the result of that scan kept too", kept)
	}
}

func TestAScanThatLearnedNothingLeavesTheListOfRegionsAlone(t *testing.T) {
	scanned := []psiphon.ScanResult{{Region: "DE", Connected: true}}
	cases := []struct {
		name    string
		regions []string
		final   psiphon.ScanStatus
	}{
		{"cancelled", nil, psiphon.ScanStatus{Cancelled: true, Results: scanned}},
		{"failed to ask", nil, psiphon.ScanStatus{Error: "could not learn which regions have servers: no list within 45s"}},
		{"named regions", []string{"DE"}, psiphon.ScanStatus{Total: 1, Results: scanned}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := useScans(t)
			svc := &PsiphonService{}
			if err := svc.ScanRegions(tc.regions); err != nil {
				t.Fatalf("ScanRegions: %v", err)
			}

			fake.onFinish(tc.final)

			for _, r := range svc.Regions() {
				if r.Available != nil {
					t.Fatalf("%s is marked after a scan that learned nothing, want the regions left as they were", r.Code)
				}
			}
		})
	}
}

func TestUninstallForgetsWhatTheInstallBeforeItMeasured(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.ScanRegions(nil); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}
	fake.onFinish(psiphon.ScanStatus{
		Total: 1, StartedAt: 1, FinishedAt: 2,
		Results: []psiphon.ScanResult{{Region: "DE", Connected: true, LatencyMs: 62}},
	})
	if st := svc.ScanStatus(); len(st.Results) != 1 || availability(svc.Regions())["DE"] != "yes" {
		t.Fatalf("status = %+v, want the scan and the list of regions it learned kept before the uninstall", st)
	}

	if err := svc.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if st := svc.ScanStatus(); st.StartedAt != 0 || len(st.Results) != 0 {
		t.Errorf("status = %+v after an uninstall, want nothing of the scan that the install before it ran", st)
	}
	for _, r := range svc.Regions() {
		if r.Available != nil {
			t.Fatalf("%s is marked after an uninstall, want the list of regions forgotten", r.Code)
		}
	}
}

func TestAListWithNoRegionsInItDoesNotReplaceTheOneThatIsKept(t *testing.T) {
	useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	svc := &PsiphonService{}
	svc.rememberRegions([]string{"US", "DE"})

	svc.rememberRegions(nil)

	got := availability(svc.Regions())
	if got["DE"] != "yes" || got["US"] != "yes" || got["FR"] != "no" {
		t.Fatalf("after a list of nothing: DE %s, US %s, FR %s, want the list that was kept before it", got["DE"], got["US"], got["FR"])
	}
}

func TestScanStatusKeepsTheLastResultThroughARestart(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.ScanRegions([]string{"DE", "US"}); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}
	finished := psiphon.ScanStatus{
		Total: 2, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{
			{Region: "DE", Connected: true, ConnectMs: 4000, LatencyMs: 62, IP: "203.0.113.9", Country: "DE"},
			{Region: "US", Error: "did not connect within 1m0s"},
		},
	}
	fake.onFinish(finished)

	// The panel starts again: the scanner has not run a scan in this process.
	fake.live = psiphon.ScanStatus{}
	got := svc.ScanStatus()

	if got.Running || got.Total != 2 || got.FinishedAt != 160 || len(got.Results) != 2 {
		t.Fatalf("status = %+v, want the last scan as it was kept", got)
	}
	if de := got.Results[0]; de.Region != "DE" || de.LatencyMs != 62 || de.IP != "203.0.113.9" || !de.Connected {
		t.Errorf("DE = %+v, want what the scan measured", de)
	}
	if us := got.Results[1]; us.Connected || us.Error == "" {
		t.Errorf("US = %+v, want the reason it did not connect", us)
	}
	if got.Current == nil {
		t.Error("current is null, want a list the page can read")
	}
}

func TestScanStatusIsTheRunningScanWhileOneRuns(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.ScanRegions([]string{"DE"}); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}
	fake.onFinish(psiphon.ScanStatus{Total: 1, StartedAt: 100, FinishedAt: 130, Results: []psiphon.ScanResult{{Region: "DE", Connected: true}}})

	fake.live = psiphon.ScanStatus{Running: true, Total: 5, StartedAt: 200, Current: []string{"US"}}
	got := svc.ScanStatus()

	if !got.Running || got.Total != 5 || !slices.Equal(got.Current, []string{"US"}) {
		t.Fatalf("status = %+v, want the scan that runs now and not the one that was kept", got)
	}
}

func TestScanStatusTrustsNothingInTheKeptResult(t *testing.T) {
	useScans(t)
	svc := &PsiphonService{}

	if err := svc.SetPsiphonRegionScan(`{"running":true,"total":3,"results":[{"region":"DE","connected":true},{"region":"../x"},{"region":"de"}]}`); err != nil {
		t.Fatal(err)
	}
	got := svc.ScanStatus()
	if got.Running || len(got.Results) != 1 || got.Results[0].Region != "DE" {
		t.Fatalf("status = %+v, want a scan that is not running and only the result of a region", got)
	}

	if err := svc.SetPsiphonRegionScan(`{"total":0,"startedAt":5}`); err != nil {
		t.Fatal(err)
	}
	if got = svc.ScanStatus(); got.Results == nil || got.Current == nil || got.StartedAt != 5 {
		t.Fatalf("status = %+v, want the lists empty and not null, so that the page can read them", got)
	}

	if err := svc.SetPsiphonRegionScan(`{broken`); err != nil {
		t.Fatal(err)
	}
	got = svc.ScanStatus()
	if got.Running || got.StartedAt != 0 || len(got.Results) != 0 || got.Results == nil {
		t.Fatalf("status = %+v, want nothing, and a list that is empty and not null, after a result that cannot be read", got)
	}
}
