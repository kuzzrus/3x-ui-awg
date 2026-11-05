package integration

import (
	"errors"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

var firstOfNovember = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)

func TestTheWeeklyScanIsOffUntilItIsSwitchedOn(t *testing.T) {
	useScans(t)
	svc := &PsiphonService{}
	if svc.ScanStatus().Weekly {
		t.Fatal("the weekly scan is on before anyone switched it on")
	}

	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}
	if !svc.ScanStatus().Weekly {
		t.Fatal("the weekly scan is off after it was switched on")
	}

	if err := svc.SetScanWeekly(false); err != nil {
		t.Fatal(err)
	}
	if svc.ScanStatus().Weekly {
		t.Fatal("the weekly scan is on after it was switched off")
	}
}

func TestNoScanStartsByItselfWhileTheWeeklyScanIsOff(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}

	started, err := svc.ScanIfDue(firstOfNovember)

	if started || err != nil || len(fake.asked) != 0 {
		t.Fatalf("started = %v, err = %v, asked %v, want nothing with the weekly scan off", started, err, fake.asked)
	}
}

func TestTheWeeklyScanStartsAScanOfEveryRegionWhenItIsDue(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}

	started, err := svc.ScanIfDue(firstOfNovember)

	if !started || err != nil {
		t.Fatalf("started = %v, err = %v, want a scan, since none has run by itself yet", started, err)
	}
	if len(fake.asked) != 1 || len(fake.asked[0]) != len(isoCountries) || !fake.every[0] {
		t.Fatalf("asked %d scans of %v, every = %v, want one scan of every region of the picker", len(fake.asked), fake.asked, fake.every)
	}
	if got := svc.ScanStatus().AutoAt; got != firstOfNovember.Unix() {
		t.Errorf("autoAt = %d, want the time the scan was started, %d", got, firstOfNovember.Unix())
	}
}

func TestTheWeeklyScanWaitsAWeekFromTheLastOneItStarted(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}
	if started, err := svc.ScanIfDue(firstOfNovember); !started || err != nil {
		t.Fatalf("started = %v, err = %v", started, err)
	}

	early := firstOfNovember.Add(7*24*time.Hour - time.Minute)
	if started, err := svc.ScanIfDue(early); started || err != nil {
		t.Fatalf("a minute short of a week: started = %v, err = %v, want it to wait", started, err)
	}
	due := firstOfNovember.Add(7 * 24 * time.Hour)
	if started, err := svc.ScanIfDue(due); !started || err != nil {
		t.Fatalf("a week after: started = %v, err = %v, want a scan", started, err)
	}
	if len(fake.asked) != 2 {
		t.Errorf("%d scans were asked for, want the two that were due", len(fake.asked))
	}
}

func TestTheWeeklyScanKeepsItsDayWhenTheScanCannotStart(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}
	fake.refuse = errors.New("a scan is already running")

	started, err := svc.ScanIfDue(firstOfNovember)

	if started || err == nil {
		t.Fatalf("started = %v, err = %v, want the refusal passed on", started, err)
	}
	if got := svc.ScanStatus().AutoAt; got != 0 {
		t.Errorf("autoAt = %d, want no day written for a scan that did not start, so that the next hour tries again", got)
	}

	fake.refuse = nil
	if started, err := svc.ScanIfDue(firstOfNovember.Add(time.Hour)); !started || err != nil {
		t.Fatalf("an hour later: started = %v, err = %v, want the scan that was due", started, err)
	}
}

func TestUninstallSwitchesTheWeeklyScanOff(t *testing.T) {
	useScans(t)
	svc := &PsiphonService{}
	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}
	if started, err := svc.ScanIfDue(firstOfNovember); !started || err != nil {
		t.Fatalf("started = %v, err = %v", started, err)
	}

	if err := svc.Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if st := svc.ScanStatus(); st.Weekly || st.AutoAt != 0 {
		t.Fatalf("status = %+v after an uninstall, want the weekly scan off and no day of the scan before it", st)
	}
}

func TestARegionRemembersWhenItFirstConnected(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, LatencyMs: 66}},
	})

	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 200, FinishedAt: 230,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, LatencyMs: 40}},
	})

	fi, ok := seenOf(svc, "FI")
	if !ok || fi.At != 230 || fi.FirstAt != 160 {
		t.Fatalf("FI = %+v (kept: %v), want the newer success with the time of the first one", fi, ok)
	}
}

func TestARecordFromBeforeTheFirstTimeWasKeptTakesItsOwnTimeAsThatOfTheFirst(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	if err := svc.SetPsiphonRegionsConnected(`[{"region":"FI","at":100,"latencyMs":66}]`); err != nil {
		t.Fatal(err)
	}

	finishedScan(t, fake, svc, []string{"FI"}, psiphon.ScanStatus{
		Total: 1, StartedAt: 200, FinishedAt: 230,
		Results: []psiphon.ScanResult{{Region: "FI", Connected: true, LatencyMs: 40}},
	})

	if fi, _ := seenOf(svc, "FI"); fi.FirstAt != 100 {
		t.Fatalf("FI = %+v, want the first time to be the time of the record that was kept (100)", fi)
	}
}
