package integration

import (
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/eventbus"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// listenForEvents gives the service a bus for the length of a test and returns what reaches a subscriber of it. The bus hands events
// over by itself, so a test that expects none publishes a marker behind the call and finds it first.
func listenForEvents(t *testing.T) chan eventbus.Event {
	t.Helper()
	bus := eventbus.New(0)
	got := make(chan eventbus.Event, 8)
	bus.Subscribe("psiphon-announce-test", func(e eventbus.Event) { got <- e })
	previous := EventBus
	EventBus = bus
	t.Cleanup(func() {
		EventBus = previous
		bus.Stop()
	})
	return got
}

const eventMarker eventbus.EventType = "test.marker"

func nextEvent(t *testing.T, got chan eventbus.Event) eventbus.Event {
	t.Helper()
	select {
	case e := <-got:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event within two seconds")
		return eventbus.Event{}
	}
}

// expectNoAnnouncement fails when the first thing the bus hands over is not a marker published after the scan has ended.
func expectNoAnnouncement(t *testing.T, got chan eventbus.Event) {
	t.Helper()
	EventBus.Publish(eventbus.Event{Type: eventMarker})
	if e := nextEvent(t, got); e.Type != eventMarker {
		t.Fatalf("the scan announced %+v, want nothing", e)
	}
}

// weeklyScanFinished runs the weekly scan through the fake scanner and hands its service the result as the scanner would.
func weeklyScanFinished(t *testing.T, fake *answeringScans, svc *PsiphonService, final psiphon.ScanStatus) {
	t.Helper()
	if err := svc.SetScanWeekly(true); err != nil {
		t.Fatal(err)
	}
	if started, err := svc.ScanIfDue(firstOfNovember); !started || err != nil {
		t.Fatalf("started = %v, err = %v, want the weekly scan to start", started, err)
	}
	fake.onFinish(final)
	fake.live = psiphon.ScanStatus{}
}

// anEarlierScanFoundGermany leaves a history that the scan under test is compared with.
func anEarlierScanFoundGermany(t *testing.T, fake *answeringScans, svc *PsiphonService) {
	t.Helper()
	finishedScan(t, fake, svc, []string{"DE"}, psiphon.ScanStatus{
		StartedAt: 100, FinishedAt: 160,
		Results: []psiphon.ScanResult{{Region: "DE", Connected: true}},
	})
}

func TestTheWeeklyScanAnnouncesTheRegionsThatConnectedInItForTheFirstTime(t *testing.T) {
	fake := useScans(t)
	events := listenForEvents(t)
	svc := &PsiphonService{}
	anEarlierScanFoundGermany(t, fake, svc)

	weeklyScanFinished(t, fake, svc, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260,
		Results: []psiphon.ScanResult{
			{Region: "US", Connected: true},
			{Region: "DE", Connected: true},
			{Region: "FR", Connected: true},
			{Region: "JP", Error: "did not connect within 30s"},
		},
	})

	e := nextEvent(t, events)
	data, _ := e.Data.(*eventbus.PsiphonRegionsData)
	want := []eventbus.PsiphonRegion{{Code: "FR", Name: "France"}, {Code: "US", Name: "United States"}}
	if e.Type != eventbus.EventPsiphonRegionsNew || e.Source != "psiphon" || data == nil || !slices.Equal(data.Regions, want) {
		t.Fatalf("event = %+v (%+v), want the regions that no scan before had connected, by code: %v", e, data, want)
	}
}

func TestTheFirstScanThatConnectsAnythingAnnouncesNothing(t *testing.T) {
	fake := useScans(t)
	events := listenForEvents(t)
	svc := &PsiphonService{}

	weeklyScanFinished(t, fake, svc, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260,
		Results: []psiphon.ScanResult{{Region: "US", Connected: true}, {Region: "DE", Connected: true}},
	})

	expectNoAnnouncement(t, events)
}

func TestAScanThatIsStartedByHandAnnouncesNothing(t *testing.T) {
	fake := useScans(t)
	events := listenForEvents(t)
	svc := &PsiphonService{}
	anEarlierScanFoundGermany(t, fake, svc)

	finishedScan(t, fake, svc, []string{everyRegion}, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260,
		Results: []psiphon.ScanResult{{Region: "FR", Connected: true}},
	})

	expectNoAnnouncement(t, events)
}

func TestAWeeklyScanThatWasCancelledAnnouncesNothing(t *testing.T) {
	fake := useScans(t)
	events := listenForEvents(t)
	svc := &PsiphonService{}
	anEarlierScanFoundGermany(t, fake, svc)

	weeklyScanFinished(t, fake, svc, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260, Cancelled: true,
		Results: []psiphon.ScanResult{{Region: "FR", Connected: true}},
	})

	expectNoAnnouncement(t, events)
}

func TestARegionThatTheHistoryCouldNotKeepIsNotAnnounced(t *testing.T) {
	fake := useScans(t)
	events := listenForEvents(t)
	svc := &PsiphonService{}
	anEarlierScanFoundGermany(t, fake, svc)
	refuseWrites(t, "NEW.key = 'psiphonRegionsConnected'")

	weeklyScanFinished(t, fake, svc, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260,
		Results: []psiphon.ScanResult{{Region: "FR", Connected: true}},
	})

	// Announced now, a region that was not kept would be announced again by the next scan.
	expectNoAnnouncement(t, events)
}

func TestTheWeeklyScanKeepsWhatItFoundWhenThePanelHasNoBus(t *testing.T) {
	fake := useScans(t)
	svc := &PsiphonService{}
	anEarlierScanFoundGermany(t, fake, svc)
	previous := EventBus
	EventBus = nil
	t.Cleanup(func() { EventBus = previous })

	weeklyScanFinished(t, fake, svc, psiphon.ScanStatus{
		StartedAt: 200, FinishedAt: 260,
		Results: []psiphon.ScanResult{{Region: "FR", Connected: true}},
	})

	if _, ok := seenOf(svc, "FR"); !ok {
		t.Fatal("France is not among the regions kept from the scan")
	}
}

func TestARegionIsNamedByThePickerOrByItsCode(t *testing.T) {
	if got := regionName("DE"); got != "Germany" {
		t.Errorf("regionName(DE) = %q, want Germany", got)
	}
	if got := regionName("ZZ"); got != "ZZ" {
		t.Errorf("regionName(ZZ) = %q, want the code of a region that the picker does not list", got)
	}
}
