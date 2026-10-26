// Package psiphon manages Psiphon ConsoleClient sidecar processes for the whole panel install (not one per
// inbound, unlike mtproto/amneziawgnet): the default instance on a fixed loopback port, the shape of
// internal/tor, and up to MaxCountries country tunnels beside it, each a process of its own with its own
// port, so that traffic can leave through several countries at once.
//
// Unlike internal/tor, which expects a system-installed binary, Psiphon is not
// packaged by any distro -- this package downloads an official release itself,
// the same shape as internal/adguard. Unlike both, this package never
// generates working credentials: Psiphon's PropagationChannelId/SponsorId/
// server-list access are Psiphon Inc.'s to issue, not ours to invent. Install
// seeds a bundled reference config on first use (see ensureDefaultConfig for
// where that config actually comes from) so Psiphon works without a manual
// step, but an admin's own psiphon.config -- via SaveConfig, same as always
// -- is never overwritten by it. Either way, this package only patches the
// handful of fields it must control regardless of the config's origin (see
// applyForcedFields).
package psiphon

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// SocksPort is the fixed loopback port the default Psiphon process listens
// on. Deliberately not 1080 -- an admin may already have something bound there.
const SocksPort = 19060

// startupTimeout bounds how long Start waits for the SOCKS listener, which
// Psiphon binds before any network activity -- well before a tunnel connects.
const startupTimeout = 10 * time.Second

// superviseEvery is how often the supervisor looks at the tunnels; a variable so tests can shorten it.
var superviseEvery = 10 * time.Second

func configDir() string   { return config.GetBinFolderPath() + "/psiphon" }
func dataDir() string     { return configDir() + "/data" }
func ConfigPath() string  { return configDir() + "/psiphon.config" }
func NoticesPath() string { return configDir() + "/notices.log" }

// IsConfigured reports whether the admin has supplied a psiphon.config yet,
// mirroring adguard.IsConfigured.
func IsConfigured() bool {
	info, err := os.Stat(ConfigPath())
	return err == nil && info.Mode().IsRegular()
}

// countrySlot is one country tunnel and the pair it was built for.
type countrySlot struct {
	country Country
	inst    *instance
}

// Manager owns the Psiphon processes of the whole install: the default one and the country tunnels.
type Manager struct {
	def *instance

	mu          sync.Mutex // guards countries and supervising
	countries   map[string]*countrySlot
	supervising bool
}

var (
	managerOnce sync.Once
	manager     *Manager
)

// GetManager returns the process-wide Psiphon manager singleton.
func GetManager() *Manager {
	managerOnce.Do(func() { manager = newManager() })
	return manager
}

func newManager() *Manager {
	return &Manager{def: newDefaultInstance(), countries: map[string]*countrySlot{}}
}

// Start launches the default Psiphon process if it is not already running, waiting until its
// SOCKS listener accepts connections. A no-op, not an error, when already up.
func (m *Manager) Start() error { return m.def.start(time.Now()) }

// Stop stops the default Psiphon process if it is running. A no-op, not an error, when already
// stopped, for the same symmetry reason as Start.
func (m *Manager) Stop() error { return m.def.stop() }

// IsRunning reports whether the default process is alive, not whether a tunnel is
// connected -- the same distinction internal/tor draws. See CurrentTunnel.
func (m *Manager) IsRunning() bool { return m.def.isRunning() }

// LastResult returns the last log line or exit error from the most recent default
// Psiphon process, or why it could not start, or "" if none has run yet this session.
func (m *Manager) LastResult() string { return m.def.result() }

// Restart stops and starts the default Psiphon process, used after SaveConfig changes EgressRegion
// or the config itself -- Psiphon does not hot-reload either.
func (m *Manager) Restart() error { return m.def.restart(time.Now()) }

// StopAll stops every managed Psiphon process, the default one and the countries, matching every other
// sidecar manager's StopAll() shape so web.go's shutdown sequence can call it the same way. They stop
// together: each one waits for its process to end.
func (m *Manager) StopAll() {
	var wg sync.WaitGroup
	for _, in := range m.instances() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = in.stop()
		}()
	}
	wg.Wait()
}

// instances is the default instance and every country, a snapshot.
func (m *Manager) instances() []*instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := make([]*instance, 0, len(m.countries)+1)
	all = append(all, m.def)
	for _, slot := range m.countries {
		all = append(all, slot.inst)
	}
	return all
}

// countryInstance is the instance of c, made when there is none. A country that is still running cannot be
// given another port from under its process.
func (m *Manager) countryInstance(c Country) (*instance, error) {
	if _, err := countrySpec(c); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if slot, ok := m.countries[c.Region]; ok {
		if slot.country == c {
			return slot.inst, nil
		}
		if slot.inst.isRunning() {
			return nil, fmt.Errorf("%s is running on port %d: stop it before giving it port %d", c.Region, slot.country.Port, c.Port)
		}
	}
	slot := &countrySlot{country: c, inst: newCountryInstance(c)}
	m.countries[c.Region] = slot
	return slot.inst, nil
}

func (m *Manager) existingCountry(region string) *instance {
	m.mu.Lock()
	defer m.mu.Unlock()
	if slot, ok := m.countries[region]; ok {
		return slot.inst
	}
	return nil
}

// StartCountry starts the tunnel of c, waiting until its SOCKS listener accepts connections, and keeps it up
// from then on. Starting one that is up changes nothing. A start that fails leaves nothing behind to retry.
func (m *Manager) StartCountry(c Country) error {
	in, err := m.countryInstance(c)
	if err != nil {
		return err
	}
	return in.start(time.Now())
}

// StopCountry stops the tunnel of a region and keeps it down. A no-op for one that was never started.
func (m *Manager) StopCountry(region string) error {
	if in := m.existingCountry(region); in != nil {
		return in.stop()
	}
	return nil
}

// RemoveCountry stops the tunnel of a region and deletes its files.
func (m *Manager) RemoveCountry(region string) error {
	if !ValidRegion(region) {
		return fmt.Errorf("%q is not a two-letter region code", region)
	}
	var stopErr error
	if in := m.existingCountry(region); in != nil {
		stopErr = in.stop()
		m.mu.Lock()
		delete(m.countries, region)
		m.mu.Unlock()
	}
	if err := os.RemoveAll(countryDir(region)); err != nil {
		return errors.Join(stopErr, fmt.Errorf("cannot remove %s: %w", countryDir(region), err))
	}
	return stopErr
}

// CountryState is how the tunnel of a region stands; the zero state for one that was never started.
func (m *Manager) CountryState(region string) InstanceState {
	if in := m.existingCountry(region); in != nil {
		return in.state()
	}
	return InstanceState{}
}

// forgetCountries drops every country from the manager, for an uninstall that has stopped them and removed their files.
func (m *Manager) forgetCountries() {
	m.mu.Lock()
	defer m.mu.Unlock()
	clear(m.countries)
}

// RestartCountries restarts the country tunnels that are running, which is how a new base config reaches them.
func (m *Manager) RestartCountries() error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, in := range m.instances() {
		if in == m.def || !in.isRunning() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := in.restart(time.Now()); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Supervise starts the supervisor, which looks at the tunnels every superviseEvery and starts again one that
// stopped without being told to. It is for the panel; an agent runs its own supervision of its helper
// programs, so nothing starts it there. Calling it again changes nothing.
func (m *Manager) Supervise() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.supervising {
		return
	}
	m.supervising = true
	go func() {
		ticker := time.NewTicker(superviseEvery)
		defer ticker.Stop()
		for now := range ticker.C {
			m.checkAll(now)
		}
	}()
}

// checkAll is one look of the supervisor at every tunnel; they are looked at together, since a restart waits
// for the listener of its process.
func (m *Manager) checkAll(now time.Time) {
	var wg sync.WaitGroup
	for _, in := range m.instances() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in.check(now)
		}()
	}
	wg.Wait()
}
