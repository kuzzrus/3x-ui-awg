package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// storedCountry is one country as the setting keeps it. Enabled is whether it should run, restored at boot; the port
// is chosen when it is added and stays, so an outbound that points at it never has to move.
type storedCountry struct {
	Region  string `json:"region"`
	Port    int    `json:"port"`
	Enabled bool   `json:"enabled"`
}

// tunnelOps is what the service asks of the Psiphon manager; a variable so that tests can answer for it.
type tunnelOps struct {
	start  func(c psiphon.Country) error
	stop   func(region string) error
	remove func(region string) error
	state  func(region string) psiphon.InstanceState
	probe  func(ctx context.Context, port int) psiphon.ProbeResult
}

var tunnels = tunnelOps{
	start:  func(c psiphon.Country) error { return psiphon.GetManager().StartCountry(c) },
	stop:   func(region string) error { return psiphon.GetManager().StopCountry(region) },
	remove: func(region string) error { return psiphon.GetManager().RemoveCountry(region) },
	state:  func(region string) psiphon.InstanceState { return psiphon.GetManager().CountryState(region) },
	probe:  psiphon.Probe,
}

// regionLocks keep a start, a stop and a removal of one region from interleaving, so that a start which read the
// list before a removal cannot bring the country back after it. Other regions do not wait for each other.
var regionLocks sync.Map // region -> *sync.Mutex

func lockRegion(region string) (unlock func()) {
	lock, _ := regionLocks.LoadOrStore(region, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// countriesMu serializes the read, change and write of the stored list: PsiphonService is a value, made afresh for
// each request, so a lock of its own would guard nothing.
var countriesMu sync.Mutex

// CountryStatus is one country tunnel as the UI shows it. Error is why the last start failed; LastLog is the
// last thing the process said.
type CountryStatus struct {
	Region  string               `json:"region"`
	Port    int                  `json:"port"`
	Enabled bool                 `json:"enabled"`
	Running bool                 `json:"running"`
	Tunnel  psiphon.TunnelStatus `json:"tunnel"`
	LastLog string               `json:"lastLog,omitempty"`
	Error   string               `json:"error,omitempty"`
}

// CountriesView is the answer of the list: the countries, and how many there may be.
type CountriesView struct {
	Max       int             `json:"max"`
	Countries []CountryStatus `json:"countries"`
}

// CountryProbe is the probe of one country: its latency and exit, or why there are none.
type CountryProbe struct {
	Region string `json:"region"`
	psiphon.ProbeResult
}

// loadCountries reads the stored list, dropping an entry this package would not hand out, or a repeat: the region
// becomes a path and the port something the panel listens on, so a hand-edited setting must not name either.
func (s *PsiphonService) loadCountries() ([]storedCountry, error) {
	raw, err := s.GetPsiphonCountries()
	if err != nil {
		return nil, err
	}
	var list []storedCountry
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		return nil, fmt.Errorf("the stored list of Psiphon countries is unreadable: %w", err)
	}
	seen := map[string]bool{}
	return slices.DeleteFunc(list, func(c storedCountry) bool {
		if !psiphon.ValidRegion(c.Region) || !psiphon.ValidCountryPort(c.Port) || seen[c.Region] {
			logger.Warningf("psiphon: ignoring the stored country %+v", c)
			return true
		}
		seen[c.Region] = true
		return false
	}), nil
}

func (s *PsiphonService) saveCountries(list []storedCountry) error {
	if list == nil {
		list = []storedCountry{}
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return s.SetPsiphonCountries(string(raw))
}

func countryOf(c storedCountry) psiphon.Country {
	return psiphon.Country{Region: c.Region, Port: c.Port}
}

func countryStatus(c storedCountry) CountryStatus {
	st := tunnels.state(c.Region)
	return CountryStatus{
		Region: c.Region, Port: c.Port, Enabled: c.Enabled,
		Running: st.Running, Tunnel: st.Tunnel, LastLog: st.LastLog, Error: st.Error,
	}
}

// Countries lists the country tunnels with how each stands.
func (s *PsiphonService) Countries() (CountriesView, error) {
	list, err := s.loadCountries()
	if err != nil {
		return CountriesView{}, err
	}
	view := CountriesView{Max: psiphon.MaxCountries, Countries: make([]CountryStatus, 0, len(list))}
	for _, c := range list {
		view.Countries = append(view.Countries, countryStatus(c))
	}
	return view, nil
}

// AddCountry adds a country tunnel, stopped, on the first free port of the country range, which it keeps. It
// does not start it: the UI does that next, so that a country that cannot start is still in the list to read why.
func (s *PsiphonService) AddCountry(region string) error {
	if !psiphon.ValidRegion(region) {
		return fmt.Errorf("%q is not an ISO 3166-1 alpha-2 region code in capitals", region)
	}
	countriesMu.Lock()
	defer countriesMu.Unlock()
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	if slices.ContainsFunc(list, func(c storedCountry) bool { return c.Region == region }) {
		return fmt.Errorf("%s is already in the list", region)
	}
	if len(list) >= psiphon.MaxCountries {
		return fmt.Errorf("at most %d countries at once: each is a Psiphon process of its own", psiphon.MaxCountries)
	}
	used := make(map[int]bool, len(list))
	for _, c := range list {
		used[c.Port] = true
	}
	port, err := psiphon.PickPort(used)
	if err != nil {
		return err
	}
	return s.saveCountries(append(list, storedCountry{Region: region, Port: port}))
}

// RemoveCountry stops a country tunnel, deletes its files and takes it out of the list.
func (s *PsiphonService) RemoveCountry(region string) error {
	defer lockRegion(region)()
	if err := tunnels.remove(region); err != nil {
		return err
	}
	countriesMu.Lock()
	defer countriesMu.Unlock()
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	return s.saveCountries(slices.DeleteFunc(list, func(c storedCountry) bool { return c.Region == region }))
}

// pick is the countries a request names: one, or every country for an empty region.
func pick(list []storedCountry, region string) ([]storedCountry, error) {
	if region == "" {
		return slices.Clone(list), nil
	}
	for _, c := range list {
		if c.Region == region {
			return []storedCountry{c}, nil
		}
	}
	return nil, fmt.Errorf("%s is not in the list of countries", region)
}

// forEach runs fn for every country at once, since a start waits for its listener and a stop for its process,
// and returns what each answered, in the order of list.
func forEach(list []storedCountry, fn func(i int, c storedCountry) error) []error {
	errs := make([]error, len(list))
	var wg sync.WaitGroup
	for i, c := range list {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = fn(i, c)
		}()
	}
	wg.Wait()
	return errs
}

// setEnabled records, for the countries in regions, whether they should run after a restart of the panel.
func (s *PsiphonService) setEnabled(regions map[string]bool) error {
	if len(regions) == 0 {
		return nil
	}
	countriesMu.Lock()
	defer countriesMu.Unlock()
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	for i := range list {
		if enabled, ok := regions[list[i].Region]; ok {
			list[i].Enabled = enabled
		}
	}
	return s.saveCountries(list)
}

func joinByRegion(list []storedCountry, errs []error) error {
	var all []error
	for i, err := range errs {
		if err != nil {
			all = append(all, fmt.Errorf("%s: %w", list[i].Region, err))
		}
	}
	return errors.Join(all...)
}

// startOne starts a country that is still in the list. The list is read under the lock of its region, which a
// removal also holds, so a country removed meanwhile is not started.
func (s *PsiphonService) startOne(region string) error {
	defer lockRegion(region)()
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	for _, c := range list {
		if c.Region == region {
			return tunnels.start(countryOf(c))
		}
	}
	return errors.New("it is no longer in the list")
}

// stopOne stops a country under the lock of its region, so it cannot overtake a start of the same one.
func stopOne(region string) error {
	defer lockRegion(region)()
	return tunnels.stop(region)
}

// StartCountries starts one country tunnel, or every one for an empty region, and remembers that they should run.
// A country that does not start keeps its reason in its status; the others are not held up by it.
func (s *PsiphonService) StartCountries(region string) error {
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	targets, err := pick(list, region)
	if err != nil {
		return err
	}
	errs := forEach(targets, func(_ int, c storedCountry) error { return s.startOne(c.Region) })
	up := map[string]bool{}
	for i, err := range errs {
		if err == nil {
			up[targets[i].Region] = true
		}
	}
	if saveErr := s.setEnabled(up); saveErr != nil {
		return errors.Join(joinByRegion(targets, errs), saveErr)
	}
	return joinByRegion(targets, errs)
}

// StopCountries stops one country tunnel, or every one for an empty region, and remembers that they stay down.
func (s *PsiphonService) StopCountries(region string) error {
	list, err := s.loadCountries()
	if err != nil {
		return err
	}
	targets, err := pick(list, region)
	if err != nil {
		return err
	}
	errs := forEach(targets, func(_ int, c storedCountry) error { return stopOne(c.Region) })
	down := map[string]bool{}
	for _, c := range targets {
		down[c.Region] = false
	}
	if saveErr := s.setEnabled(down); saveErr != nil {
		return errors.Join(joinByRegion(targets, errs), saveErr)
	}
	return joinByRegion(targets, errs)
}

// ProbeCountries measures one country tunnel, or every one for an empty region, all at once inside the one
// budget of verifyTimeout. A country that is not running is not probed and says so.
func (s *PsiphonService) ProbeCountries(region string) ([]CountryProbe, error) {
	list, err := s.loadCountries()
	if err != nil {
		return nil, err
	}
	targets, err := pick(list, region)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	results := make([]CountryProbe, len(targets))
	forEach(targets, func(i int, c storedCountry) error {
		results[i] = CountryProbe{Region: c.Region}
		if tunnels.state(c.Region).Running {
			results[i].ProbeResult = tunnels.probe(ctx, c.Port)
		} else {
			results[i].ProbeResult = psiphon.ProbeResult{Error: "not running"}
		}
		return nil
	})
	return results, nil
}

// autoStartCountries brings back at boot the countries that were running before the panel stopped. It runs
// beside the rest of the start: a country waits seconds for its listener, and nothing else waits for it.
func (s *PsiphonService) autoStartCountries() {
	list, err := s.loadCountries()
	if err != nil {
		logger.Warning("psiphon: the countries are unreadable, staying down:", err)
		return
	}
	var want []storedCountry
	for _, c := range list {
		if c.Enabled {
			want = append(want, c)
		}
	}
	if len(want) == 0 {
		return
	}
	if !psiphon.IsInstalled() || !psiphon.IsConfigured() {
		logger.Warning("psiphon: countries are enabled but Psiphon is not installed or has no config yet, staying down")
		return
	}
	for i, err := range forEach(want, func(_ int, c storedCountry) error { return s.startOne(c.Region) }) {
		if err != nil {
			logger.Warningf("psiphon[%s]: failed to auto-start on boot: %v", want[i].Region, err)
		}
	}
}
