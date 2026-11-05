package integration

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// finishedScan runs a scan of regions through the fake scanner and hands its service the result as the scanner would.
func finishedScan(t *testing.T, fake *answeringScans, svc *PsiphonService, regions []string, final psiphon.ScanStatus) {
	t.Helper()
	if err := svc.ScanRegions(regions); err != nil {
		t.Fatalf("ScanRegions: %v", err)
	}
	fake.onFinish(final)
	fake.live = psiphon.ScanStatus{}
}

func seenOf(svc *PsiphonService, region string) (psiphon.ConnectedRegion, bool) {
	for _, s := range svc.ScanStatus().Seen {
		if s.Region == region {
			return s, true
		}
	}
	return psiphon.ConnectedRegion{}, false
}

func TestAScanRemembersTheRegionsThatConnectedInIt(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"DE", "US"}, psiphon.ScanStatus{
		Total: 2, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{
			{Region: "DE", Connected: true, ConnectMs: 4000, LatencyMs: 62, IP: "203.0.113.9", Country: "DE"},
			{Region: "US", Error: "did not connect within 1m0s"},
		},
	})

	got := svc.ScanStatus().Seen

	if len(got) != 1 {
		t.Fatalf("seen = %+v, want the one region that connected", got)
	}
	want := psiphon.ConnectedRegion{Region: "DE", At: 160, ConnectMs: 4000, LatencyMs: 62, IP: "203.0.113.9", Country: "DE"}
	if got[0] != want {
		t.Errorf("DE = %+v, want %+v: what the scan measured, at the time it ended", got[0], want)
	}
}

func TestARegionStaysSeenWhenALaterScanFindsNoTunnelThere(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, ConnectMs: 7000, LatencyMs: 66}},
	})

	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 200, FinishedAt: 230,
		Results: []psiphon.ScanResult{{Region: "FI", Error: "did not connect within 30s"}},
	})

	fi, ok := seenOf(svc, "FI")
	if !ok || fi.At != 160 || fi.LatencyMs != 66 {
		t.Fatalf("FI = %+v (kept: %v), want the success of the first scan, which a failure does not erase", fi, ok)
	}
}

func TestANewSuccessReplacesTheOneKeptOfARegion(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, ConnectMs: 7000, LatencyMs: 66}},
	})

	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 200, FinishedAt: 230,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, ConnectMs: 5000, LatencyMs: 40}},
	})

	if n := len(svc.ScanStatus().Seen); n != 1 {
		t.Fatalf("%d entries, want one for the region", n)
	}
	if fi, _ := seenOf(svc, "FI"); fi.At != 230 || fi.LatencyMs != 40 {
		t.Fatalf("FI = %+v, want the newer success", fi)
	}
}

func TestARegionSeenInAnEarlierScanIsKeptByAScanOfOtherRegions(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"DE"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "DE", Connected: true, LatencyMs: 62}},
	})

	finishedScan(t, fake, svc, []string{"FR"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 200, FinishedAt: 230,
		Results: []psiphon.ScanResult{{Region: "FR", Connected: true, LatencyMs: 90}},
	})

	_, de := seenOf(svc, "DE")
	_, fr := seenOf(svc, "FR")
	if !de || !fr {
		t.Fatalf("seen = %+v, want DE from the first scan and FR from the second", svc.ScanStatus().Seen)
	}
}

func TestACancelledScanKeepsWhatItHadMeasured(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}

	finishedScan(t, fake, svc, []string{"DE", "US"}, psiphon.ScanStatus{
		Total: 2, StartedAt: 100, FinishedAt: 130, Cancelled: true,
		Results: []psiphon.ScanResult{{Region: "DE", Connected: true, LatencyMs: 62}},
	})

	if _, ok := seenOf(svc, "DE"); !ok {
		t.Fatal("DE connected before the scan was cancelled, want it kept")
	}
}

func TestAScanThatConnectedNowhereLeavesTheKeptRegionsAlone(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}

	finishedScan(t, fake, svc, []string{"DE"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 130,
		Results: []psiphon.ScanResult{{Region: "DE", Error: "did not connect within 1m0s"}},
	})

	if raw, err := svc.GetPsiphonRegionsConnected(); err != nil || raw != "" {
		t.Fatalf("kept regions = %q (%v), want nothing written by a scan in which no tunnel came up", raw, err)
	}
}

func TestSeenRegionsAreThereWhileAScanRuns(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"DE"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "DE", Connected: true, LatencyMs: 62}},
	})

	fake.live = psiphon.ScanStatus{Running: true, Total: 5, StartedAt: 200, Current: []string{"US"}}
	got := svc.ScanStatus()

	if !got.Running || len(got.Seen) != 1 || got.Seen[0].Region != "DE" {
		t.Fatalf("status = %+v, want the scan that runs, and DE from the earlier one with it", got)
	}
}

func TestSeenRegionsTrustNothingInTheKeptList(t *testing.T) {
	useScans(t)
	svc := &PsiphonService{}

	if err := svc.SetPsiphonRegionsConnected(`[{"region":"DE","at":5},{"region":"DE","at":9,"latencyMs":40},{"region":"../x","at":9},{"region":"de","at":9},{"region":"FR","at":0},{"region":"US","at":3}]`); err != nil {
		t.Fatal(err)
	}
	got := svc.ScanStatus().Seen
	if len(got) != 2 || got[0].Region != "DE" || got[0].At != 9 || got[0].LatencyMs != 40 || got[1].Region != "US" {
		t.Fatalf("seen = %+v, want DE once with its newest entry, then US, and nothing of the entries that are no region or have no time", got)
	}

	for _, kept := range []string{`{broken`, `null`, `[]`} {
		if err := svc.SetPsiphonRegionsConnected(kept); err != nil {
			t.Fatal(err)
		}
		if got = svc.ScanStatus().Seen; got == nil || len(got) != 0 {
			t.Fatalf("seen = %#v after the kept list %s, want an empty list and not null, so that the page can read it", got, kept)
		}
	}
}
