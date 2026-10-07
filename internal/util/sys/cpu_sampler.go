package sys

import "sync"

// cpuSmoothing is the weight of the newest sample in the moving average.
const cpuSmoothing = 0.3

// CPUSampler reports CPU use the way the panel charts it: the native counter smoothed
// with a moving average. The first call only sets the baseline and returns 0.
type CPUSampler struct {
	mu      sync.Mutex
	raw     func() (float64, error) // CPUPercentRaw unless a test swaps it
	primed  bool
	average float64
}

// Percent samples the native counter. An error means this host has none to read.
func (s *CPUSampler) Percent() (float64, error) {
	raw := s.raw
	if raw == nil {
		raw = CPUPercentRaw
	}
	pct, err := raw()
	if err != nil {
		return 0, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.primed {
		s.primed = true
		return 0, nil
	}
	if s.average == 0 {
		s.average = pct
	} else {
		s.average = cpuSmoothing*pct + (1-cpuSmoothing)*s.average
	}
	return s.average, nil
}
