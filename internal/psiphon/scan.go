package psiphon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"
)

// The loopback ports a scan draws from: away from the default instance, from Tor and from the country tunnels.
const (
	scanPortFirst = 19201
	scanPortLast  = 19299
)

// How a scan is paced. The values are variables so tests can shorten them.
var (
	// scanParallel caps the temporary tunnels up at once: each is a process of about 40 MB.
	scanParallel = 3
	// scanConnectTimeout is how long a region gets to connect; one usually takes seconds, a slow one most of a minute.
	scanConnectTimeout = 60 * time.Second
	// scanAllConnectTimeout is the time a region gets in a scan of every region, where most of the 200 and more have no servers.
	scanAllConnectTimeout = 30 * time.Second
	// scanDiscoverTimeout is how long a tunnel on a store that has servers gets to name their regions: it does so at once.
	scanDiscoverTimeout = 15 * time.Second
	scanPoll            = 500 * time.Millisecond
)

// ScanOption changes how one scan is run.
type ScanOption func(*scanSettings)

// scanSettings are the parts of a scan that its options change.
type scanSettings struct{ connectTimeout time.Duration }

// OfEveryRegion marks a scan of every region of the picker: most of them have no servers and wait out their time, so each gets less.
func OfEveryRegion() ScanOption {
	return func(c *scanSettings) { c.connectTimeout = scanAllConnectTimeout }
}

// ScanResult is what a scan found of one region: whether a tunnel through it came up and how long that took,
// and what a request through it showed. Error says what did not work; a latency may still be there with it.
type ScanResult struct {
	Region    string `json:"region"`
	Connected bool   `json:"connected"`
	ConnectMs int64  `json:"connectMs,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	IP        string `json:"ip,omitempty"`
	Country   string `json:"country,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ConnectedRegion is the last time a tunnel through a region came up in a scan, kept across scans: a region can connect in one scan
// and not in the next, since Psiphon hands each start a part of its servers.
type ConnectedRegion struct {
	Region    string `json:"region"`
	FirstAt   int64  `json:"firstAt,omitempty"`
	At        int64  `json:"at"`
	ConnectMs int64  `json:"connectMs,omitempty"`
	LatencyMs int64  `json:"latencyMs,omitempty"`
	IP        string `json:"ip,omitempty"`
	Country   string `json:"country,omitempty"`
}

// ScanStatus is how a scan stands. Results come in the order the regions finished, Current is what is being scanned now, and
// Error is why the scan as a whole could not go on. Seen, Weekly and AutoAt are for the service to fill: the scanner knows one scan.
type ScanStatus struct {
	Running     bool              `json:"running"`
	Discovering bool              `json:"discovering,omitempty"`
	Cancelled   bool              `json:"cancelled,omitempty"`
	Error       string            `json:"error,omitempty"`
	Total       int               `json:"total"`
	Current     []string          `json:"current"`
	StartedAt   int64             `json:"startedAt,omitempty"`
	FinishedAt  int64             `json:"finishedAt,omitempty"`
	Results     []ScanResult      `json:"results"`
	Seen        []ConnectedRegion `json:"seen"`
	Weekly      bool              `json:"weekly"`
	AutoAt      int64             `json:"autoAt,omitempty"`
}

// Scanner runs the scan: a temporary tunnel per region, a few at a time, each measured and taken down again.
// Only one scan runs at once.
type Scanner struct {
	mu       sync.Mutex // guards everything below
	status   ScanStatus
	settings scanSettings // how the run in progress goes
	ports    map[int]bool
	cancel   context.CancelFunc
	done     chan struct{} // closed when the run in progress has ended and cleaned up
}

func newScanner() *Scanner { return &Scanner{} }

func scanDir() string { return filepath.Join(Dir(), "scan") }

// scanSpec is where the temporary tunnel called name keeps its files: a region, or "discover" for the one that
// only asks which regions have servers.
func scanSpec(name string, port int) spec {
	dir := filepath.Join(scanDir(), name)
	return spec{
		config:  filepath.Join(dir, "psiphon.config"),
		data:    filepath.Join(dir, "data"),
		notices: filepath.Join(dir, "notices.log"),
		port:    port,
	}
}

// cleanScanRegions refuses a code that is not a region, since it names a directory, and takes each region once.
func cleanScanRegions(regions []string) ([]string, error) {
	out := make([]string, 0, len(regions))
	for _, r := range regions {
		if !ValidRegion(r) {
			return nil, fmt.Errorf("%q is not a two-letter region code", r)
		}
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Start begins a scan of regions in the background, or of those a tunnel says have servers when none is named. onFinish, which
// may be nil, gets the final status once the scan has cleaned up; the scan still reads as running until it returns.
func (s *Scanner) Start(regions []string, onFinish func(ScanStatus), opts ...ScanOption) error {
	if !IsInstalled() {
		return errors.New("Psiphon is not installed")
	}
	if !IsConfigured() {
		return errors.New("Psiphon has no config -- upload a psiphon.config first")
	}
	list, err := cleanScanRegions(regions)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Running {
		return errors.New("a scan is already running")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	s.settings = scanSettings{connectTimeout: scanConnectTimeout}
	for _, opt := range opts {
		opt(&s.settings)
	}
	s.ports = map[int]bool{}
	s.status = ScanStatus{Running: true, Discovering: len(list) == 0, Total: len(list), StartedAt: time.Now().Unix()}
	go s.run(ctx, list, s.done, onFinish)
	return nil
}

// Cancel ends the scan that runs, if one does, and returns once its tunnels are down and their files gone.
func (s *Scanner) Cancel() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Status is how the scan that runs, or the last one this process ran, stands.
func (s *Scanner) Status() ScanStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// forget drops the status of the last scan, so that an install after an uninstall does not show what the one before it
// measured. The run is over by then: StopAll ends it before the files go.
func (s *Scanner) forget() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = ScanStatus{}
}

// snapshotLocked copies the status, so what a caller keeps is not changed under it and a list is never null.
func (s *Scanner) snapshotLocked() ScanStatus {
	st := s.status
	st.Current = append([]string{}, st.Current...)
	st.Results = append([]ScanResult{}, st.Results...)
	return st
}

func (s *Scanner) run(ctx context.Context, regions []string, done chan struct{}, onFinish func(ScanStatus)) {
	defer close(done)
	var scanErr error
	if len(regions) == 0 {
		regions, scanErr = s.discover(ctx)
		s.mu.Lock()
		s.status.Discovering, s.status.Total = false, len(regions)
		s.mu.Unlock()
	}
	if scanErr == nil {
		s.scanAll(ctx, regions)
	}
	s.finish(ctx, scanErr, onFinish)
}

// scanAll scans the regions with scanParallel workers; a cancel leaves the regions not yet taken unscanned.
func (s *Scanner) scanAll(ctx context.Context, regions []string) {
	jobs := make(chan string)
	var wg sync.WaitGroup
	for range min(scanParallel, len(regions)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for region := range jobs {
				s.scanOne(ctx, region)
			}
		}()
	}
feed:
	for _, region := range regions {
		select {
		case jobs <- region:
		case <-ctx.Done():
			break feed
		}
	}
	close(jobs)
	wg.Wait()
}

// scanOne scans a region and records the result, unless the scan was cancelled meanwhile: that region is then
// unfinished, and what it found is not a measurement.
func (s *Scanner) scanOne(ctx context.Context, region string) {
	s.mu.Lock()
	s.status.Current = append(s.status.Current, region)
	s.mu.Unlock()

	result := s.scanRegion(ctx, region)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Current = slices.DeleteFunc(s.status.Current, func(r string) bool { return r == region })
	if ctx.Err() == nil {
		s.status.Results = append(s.status.Results, result)
	}
}

// scanRegion brings a tunnel up in region, waits for it to connect and measures it, then takes it down.
func (s *Scanner) scanRegion(ctx context.Context, region string) ScanResult {
	result := ScanResult{Region: region}
	port, err := s.takePort()
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer s.releasePort(port)
	sp := scanSpec(region, port)
	defer os.RemoveAll(filepath.Dir(sp.config))

	began := time.Now()
	proc, err := startScanProcess(ctx, sp, region)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = proc.Stop() }()
	if err := waitForTunnel(ctx, sp.notices, proc, s.connectLimit()); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Connected, result.ConnectMs = true, time.Since(began).Milliseconds()

	probe := Probe(ctx, port)
	result.LatencyMs, result.IP, result.Country, result.Error = probe.LatencyMs, probe.IP, probe.Country, probe.Error
	return result
}

// discover returns the regions a tunnel says have servers, so that a scan of every region knows them before any tunnel has run. A
// tunnel on an empty store names none and does not say it again: one runs until it has a tunnel, and a second names them.
func (s *Scanner) discover(ctx context.Context) ([]string, error) {
	port, err := s.takePort()
	if err != nil {
		return nil, err
	}
	defer s.releasePort(port)
	sp := scanSpec("discover", port)
	defer os.RemoveAll(filepath.Dir(sp.config))

	if err := fillStore(ctx, sp); err != nil {
		return nil, fmt.Errorf("could not learn which regions have servers: %w", err)
	}
	return askStore(ctx, sp)
}

// fillStore runs a tunnel on an empty data store until it has a tunnel, which leaves the servers it found in the store.
func fillStore(ctx context.Context, sp spec) error {
	proc, err := startScanProcess(ctx, sp, "")
	if err != nil {
		return err
	}
	defer func() { _ = proc.Stop() }()
	return waitForTunnel(ctx, sp.notices, proc, scanConnectTimeout)
}

// askStore starts a tunnel on the store as it is and returns the regions it names.
func askStore(ctx context.Context, sp spec) ([]string, error) {
	proc, err := runScanProcess(ctx, sp)
	if err != nil {
		return nil, fmt.Errorf("could not learn which regions have servers: %w", err)
	}
	defer func() { _ = proc.Stop() }()
	deadline := time.Now().Add(scanDiscoverTimeout)
	for {
		if seen, _ := seenRegionsIn(sp.notices); len(seen.Regions) > 0 {
			return seen.Regions, nil
		}
		if !proc.IsRunning() {
			return nil, fmt.Errorf("could not learn which regions have servers: Psiphon exited: %s", proc.GetResult())
		}
		if !time.Now().Before(deadline) {
			return nil, fmt.Errorf("could not learn which regions have servers: no list within %s", scanDiscoverTimeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(scanPoll):
		}
	}
}

// startScanProcess starts a temporary tunnel in a folder of the scan made afresh, so that the notices a crashed scan left are not
// read as this tunnel's, and waits until its SOCKS listener is up. It refuses any other folder, such as the default tunnel's.
func startScanProcess(ctx context.Context, sp spec, region string) (*Process, error) {
	dir := filepath.Dir(sp.config)
	if filepath.Dir(dir) != scanDir() {
		return nil, fmt.Errorf("%s is not a folder of the scan", dir)
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("clearing %s: %w", dir, err)
	}
	if err := writeTunnelConfig(sp, region); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(sp.data, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", sp.data, err)
	}
	return runScanProcess(ctx, sp)
}

// runScanProcess starts the tunnel that the folder of sp describes, as that folder is, and waits until its SOCKS listener is up.
func runScanProcess(ctx context.Context, sp spec) (*Process, error) {
	proc := newProcess(sp, "[scan "+filepath.Base(filepath.Dir(sp.config))+"]")
	if err := proc.Start(); err != nil {
		return nil, err
	}
	if err := waitForListener(ctx, net.JoinHostPort("127.0.0.1", strconv.Itoa(sp.port)), proc); err != nil {
		_ = proc.Stop()
		return nil, err
	}
	return proc, nil
}

// connectLimit is the time a region gets to connect in the scan that runs.
func (s *Scanner) connectLimit() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings.connectTimeout
}

// waitForTunnel blocks until the process reports a tunnel, it exits, limit runs out or the scan is cancelled.
func waitForTunnel(ctx context.Context, notices string, proc *Process, limit time.Duration) error {
	deadline := time.Now().Add(limit)
	for {
		if tunnel, _ := currentTunnel(notices); tunnel.Connected {
			return nil
		}
		if !proc.IsRunning() {
			return fmt.Errorf("Psiphon exited before it connected: %s", proc.GetResult())
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("did not connect within %s", limit)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(scanPoll):
		}
	}
}

// takePort reserves the first port of the scan range that no other temporary tunnel has and nothing listens on.
func (s *Scanner) takePort() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for port := scanPortFirst; port <= scanPortLast; port++ {
		if !s.ports[port] && portFree(port) {
			s.ports[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in %d-%d for a scan", scanPortFirst, scanPortLast)
}

func (s *Scanner) releasePort(port int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.ports, port)
}

// finish clears the folder of the tunnels of a run that has ended, all down by now, and closes its status. The status reads as
// running until onFinish returns and is then replaced as a whole, so a page that sees the scan end finds what onFinish stored.
func (s *Scanner) finish(ctx context.Context, scanErr error, onFinish func(ScanStatus)) {
	_ = os.RemoveAll(scanDir())
	s.mu.Lock()
	final := s.snapshotLocked()
	final.Running, final.Discovering, final.Current = false, false, []string{}
	final.FinishedAt = time.Now().Unix()
	final.Cancelled = ctx.Err() != nil
	if scanErr != nil && !final.Cancelled {
		final.Error = scanErr.Error()
	}
	s.mu.Unlock()
	if onFinish != nil {
		onFinish(final)
	}
	s.mu.Lock()
	s.cancel()
	s.status = final
	s.mu.Unlock()
}
