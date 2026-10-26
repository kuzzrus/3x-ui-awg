package integration

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// answeringTunnels stands in for the Psiphon manager for the length of a test: starts fail for the regions in
// failing, and every call is written down.
type answeringTunnels struct {
	mu      sync.Mutex
	failing map[string]error
	running map[string]bool
	started []string
	stopped []string
}

func useTunnels(t *testing.T, failing map[string]error) *answeringTunnels {
	t.Helper()
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	fake := &answeringTunnels{failing: failing, running: map[string]bool{}}
	previous := tunnels
	t.Cleanup(func() { tunnels = previous })
	tunnels = tunnelOps{
		start: func(c psiphon.Country) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if err := fake.failing[c.Region]; err != nil {
				return err
			}
			fake.running[c.Region] = true
			fake.started = append(fake.started, c.Region)
			return nil
		},
		stop: func(region string) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			fake.running[region] = false
			fake.stopped = append(fake.stopped, region)
			return nil
		},
		remove: func(region string) error {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			delete(fake.running, region)
			return nil
		},
		state: func(region string) psiphon.InstanceState {
			fake.mu.Lock()
			defer fake.mu.Unlock()
			return psiphon.InstanceState{Running: fake.running[region]}
		},
		probe: func(_ context.Context, port int) psiphon.ProbeResult {
			return psiphon.ProbeResult{LatencyMs: int64(port % 1000), IP: "203.0.113.7", Country: "DE"}
		},
	}
	return fake
}

func regionsOf(view CountriesView) []string {
	regions := make([]string, 0, len(view.Countries))
	for _, c := range view.Countries {
		regions = append(regions, c.Region)
	}
	return regions
}

func TestAddCountryKeepsThePortItIsGivenAndRefusesWhatItShould(t *testing.T) {
	useTunnels(t, nil)
	svc := &PsiphonService{}

	for _, bad := range []string{"", "de", "DEU", "../x", "D1"} {
		if err := svc.AddCountry(bad); err == nil {
			t.Errorf("AddCountry(%q) was accepted", bad)
		}
	}
	for _, region := range []string{"DE", "US"} {
		if err := svc.AddCountry(region); err != nil {
			t.Fatalf("AddCountry(%s): %v", region, err)
		}
	}
	if err := svc.AddCountry("DE"); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("AddCountry of a repeat = %v", err)
	}

	view, err := svc.Countries()
	if err != nil {
		t.Fatalf("Countries: %v", err)
	}
	if view.Max != psiphon.MaxCountries || !slices.Equal(regionsOf(view), []string{"DE", "US"}) {
		t.Fatalf("view = %+v", view)
	}
	de, us := view.Countries[0], view.Countries[1]
	if de.Port == us.Port || !psiphon.ValidCountryPort(de.Port) || !psiphon.ValidCountryPort(us.Port) {
		t.Fatalf("ports %d and %d: want two different ports of the country range", de.Port, us.Port)
	}
	if de.Enabled || de.Running {
		t.Fatalf("a country that was only added is %+v, want it stopped", de)
	}

	// The port stays with the country across reads, and across the removal of another one.
	if err := svc.RemoveCountry("US"); err != nil {
		t.Fatalf("RemoveCountry: %v", err)
	}
	again, _ := svc.Countries()
	if len(again.Countries) != 1 || again.Countries[0].Port != de.Port {
		t.Fatalf("after removing US: %+v, want DE on its port %d", again.Countries, de.Port)
	}
}

func TestAddCountryStopsAtTheLimit(t *testing.T) {
	useTunnels(t, nil)
	svc := &PsiphonService{}
	regions := []string{"DE", "US", "JP", "GB", "FR", "NL", "SE", "IT"}
	for i, region := range regions {
		err := svc.AddCountry(region)
		if i < psiphon.MaxCountries && err != nil {
			t.Fatalf("AddCountry(%s) #%d: %v", region, i+1, err)
		}
		if i >= psiphon.MaxCountries && err == nil {
			t.Fatalf("AddCountry(%s) #%d was accepted past the limit of %d", region, i+1, psiphon.MaxCountries)
		}
	}
}

func TestStartCountriesRemembersOnlyTheOnesThatStarted(t *testing.T) {
	fake := useTunnels(t, map[string]error{"JP": errors.New("port 19102 is held by another process")})
	svc := &PsiphonService{}
	for _, region := range []string{"DE", "JP", "US"} {
		if err := svc.AddCountry(region); err != nil {
			t.Fatal(err)
		}
	}

	err := svc.StartCountries("")
	if err == nil || !strings.Contains(err.Error(), "JP: port 19102 is held by another process") {
		t.Fatalf("StartCountries = %v, want the refused country named with its reason", err)
	}
	slices.Sort(fake.started)
	if !slices.Equal(fake.started, []string{"DE", "US"}) {
		t.Fatalf("started %v: one country that cannot start must not hold up the others", fake.started)
	}
	view, _ := svc.Countries()
	enabled := map[string]bool{}
	for _, c := range view.Countries {
		enabled[c.Region] = c.Enabled
	}
	if !enabled["DE"] || !enabled["US"] || enabled["JP"] {
		t.Fatalf("enabled = %v, want the two that started to come back after a restart and not the third", enabled)
	}

	if err := svc.StartCountries("FR"); err == nil || !strings.Contains(err.Error(), "not in the list") {
		t.Fatalf("StartCountries of an unknown region = %v", err)
	}
}

func TestStopCountriesForgetsThatTheyShouldRun(t *testing.T) {
	fake := useTunnels(t, nil)
	svc := &PsiphonService{}
	for _, region := range []string{"DE", "US"} {
		if err := svc.AddCountry(region); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.StartCountries(""); err != nil {
		t.Fatalf("StartCountries: %v", err)
	}

	if err := svc.StopCountries("DE"); err != nil {
		t.Fatalf("StopCountries(DE): %v", err)
	}
	if !slices.Equal(fake.stopped, []string{"DE"}) {
		t.Fatalf("stopped %v, want only the one named", fake.stopped)
	}
	view, _ := svc.Countries()
	for _, c := range view.Countries {
		if want := c.Region == "US"; c.Enabled != want {
			t.Errorf("%s enabled = %v, want %v", c.Region, c.Enabled, want)
		}
	}

	if err := svc.StopCountries(""); err != nil {
		t.Fatalf("StopCountries(all): %v", err)
	}
	view, _ = svc.Countries()
	for _, c := range view.Countries {
		if c.Enabled || c.Running {
			t.Errorf("%s = %+v after stopping them all", c.Region, c)
		}
	}
}

func TestProbeCountriesMeasuresTheRunningOnesAndNamesTheRest(t *testing.T) {
	useTunnels(t, nil)
	svc := &PsiphonService{}
	for _, region := range []string{"DE", "US"} {
		if err := svc.AddCountry(region); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.StartCountries("DE"); err != nil {
		t.Fatal(err)
	}

	results, err := svc.ProbeCountries("")
	if err != nil {
		t.Fatalf("ProbeCountries: %v", err)
	}
	if len(results) != 2 || results[0].Region != "DE" || results[1].Region != "US" {
		t.Fatalf("results = %+v, want one per country in the order of the list", results)
	}
	if results[0].IP != "203.0.113.7" || results[0].Country != "DE" || results[0].Error != "" {
		t.Errorf("DE = %+v, want its measurement", results[0])
	}
	if results[1].Error != "not running" || results[1].IP != "" {
		t.Errorf("US = %+v, want it named as not running and not measured", results[1])
	}

	one, err := svc.ProbeCountries("DE")
	if err != nil || len(one) != 1 || one[0].Region != "DE" {
		t.Fatalf("ProbeCountries(DE) = %+v, %v", one, err)
	}
	if _, err := svc.ProbeCountries("FR"); err == nil {
		t.Fatal("ProbeCountries of an unknown region succeeded")
	}
}

// The setting is the one thing the panel keeps; whatever else it may hold is not trusted: a region becomes a
// path and a port becomes something this panel listens on.
func TestStoredCountriesThatThisPackageWouldNotHandOutAreDropped(t *testing.T) {
	useTunnels(t, nil)
	svc := &PsiphonService{}
	raw := `[{"region":"DE","port":19101,"enabled":true},` +
		`{"region":"../..","port":19102,"enabled":true},` +
		`{"region":"US","port":22,"enabled":true},` +
		`{"region":"DE","port":19103,"enabled":false},` +
		`{"region":"JP","port":19104,"enabled":false}]`
	if err := svc.SetPsiphonCountries(raw); err != nil {
		t.Fatal(err)
	}
	view, err := svc.Countries()
	if err != nil {
		t.Fatalf("Countries: %v", err)
	}
	if !slices.Equal(regionsOf(view), []string{"DE", "JP"}) {
		t.Fatalf("countries = %v, want the sound entries, the first of a repeat", regionsOf(view))
	}
	if view.Countries[0].Port != 19101 {
		t.Fatalf("DE on port %d, want the first entry to win", view.Countries[0].Port)
	}

	if err := svc.SetPsiphonCountries("not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Countries(); err == nil {
		t.Fatal("an unreadable setting was taken for an empty list")
	}
}

func TestCountriesAreEmptyBeforeAnyIsAdded(t *testing.T) {
	useTunnels(t, nil)
	view, err := (&PsiphonService{}).Countries()
	if err != nil || len(view.Countries) != 0 || view.Max != psiphon.MaxCountries {
		t.Fatalf("view = %+v, %v, want an empty list with the limit", view, err)
	}
}
