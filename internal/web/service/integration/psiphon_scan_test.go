package integration

import (
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// answeringScans stands in for the scanner for the length of a test: it writes down what it was asked, and answers
// with the status and the error it is given.
type answeringScans struct {
	mu        sync.Mutex
	asked     [][]string
	onFinish  func(psiphon.ScanStatus)
	live      psiphon.ScanStatus
	startErr  error
	cancelled int
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
			return fake.startErr
		},
		cancel: func() {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.cancelled++
		},
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

func TestScanPassesOnWhyTheScannerRefused(t *testing.T) {
	fake := useScans(t)
	fake.startErr = errors.New("a scan is already running")

	err := (&PsiphonService{}).ScanRegions([]string{"DE"})

	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("ScanRegions = %v, want the scanner's reason", err)
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

func TestCancelScanAsksTheScannerToEnd(t *testing.T) {
	fake := useScans(t)

	(&PsiphonService{}).CancelScan()

	if fake.cancelled != 1 {
		t.Fatalf("the scanner was asked to cancel %d times, want once", fake.cancelled)
	}
}
