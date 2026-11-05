package integration

import "time"

// weeklyScanEvery is how long the weekly scan waits after the last one it started.
const weeklyScanEvery = 7 * 24 * time.Hour

// SetScanWeekly switches on or off the scan of every region that starts by itself once a week.
func (s *PsiphonService) SetScanWeekly(on bool) error { return s.SetPsiphonScanWeekly(on) }

// ScanIfDue starts the scan of every region when the weekly scan is on and a week has passed since the last one it started, and
// says whether it did. A scan that cannot start (another runs, Psiphon is not installed) leaves the day alone, so the next call tries again.
func (s *PsiphonService) ScanIfDue(now time.Time) (bool, error) {
	on, err := s.GetPsiphonScanWeekly()
	if err != nil || !on {
		return false, err
	}
	last, err := s.GetPsiphonLastAutoScan()
	if err != nil {
		return false, err
	}
	if now.Sub(time.Unix(last, 0)) < weeklyScanEvery {
		return false, nil
	}
	if err := s.ScanRegions([]string{everyRegion}); err != nil {
		return false, err
	}
	return true, s.SetPsiphonLastAutoScan(now.Unix())
}

// forgetSchedule switches the weekly scan off and forgets its day, so that a new install does not start a scan nobody asked it for.
func (s *PsiphonService) forgetSchedule() error {
	if err := s.SetPsiphonScanWeekly(false); err != nil {
		return err
	}
	return s.SetPsiphonLastAutoScan(0)
}
