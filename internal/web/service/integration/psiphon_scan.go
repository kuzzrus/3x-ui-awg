package integration

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// scanOps is what the service asks of the Psiphon scanner; a variable so that tests can answer for it.
type scanOps struct {
	start  func(regions []string, every bool, onFinish func(psiphon.ScanStatus)) error
	cancel func()
	status func() psiphon.ScanStatus
}

var scans = scanOps{
	start: func(regions []string, every bool, onFinish func(psiphon.ScanStatus)) error {
		var opts []psiphon.ScanOption
		if every {
			opts = append(opts, psiphon.OfEveryRegion())
		}
		return psiphon.GetScanner().Start(regions, onFinish, opts...)
	},
	cancel: func() { psiphon.GetScanner().Cancel() },
	status: func() psiphon.ScanStatus { return psiphon.GetScanner().Status() },
}

// everyRegion is the one name in a scan's regions that is not a code: it asks for every region of the picker.
const everyRegion = "all"

// ScanRegions starts the scan of the named regions in the background, or of every region of the picker when "all" is named, or of
// every region a tunnel said has servers when none is; with no list known yet the scanner asks a tunnel, and the list it gets is kept.
func (s *PsiphonService) ScanRegions(regions []string) error {
	if len(regions) == 1 && strings.EqualFold(regions[0], everyRegion) {
		return scans.start(s.pickerRegions(), true, s.keepScan)
	}
	if len(regions) == 0 {
		regions = s.knownRegions()
	}
	if len(regions) == 0 {
		return scans.start(nil, false, s.keepLearningScan)
	}
	return scans.start(regions, false, s.keepScan)
}

// pickerRegions is every region of the picker, those a tunnel named first: they are the ones likely to connect, and a scan reports
// them in the order they finish.
func (s *PsiphonService) pickerRegions() []string {
	codes := slices.Clone(s.knownRegions())
	for _, r := range isoCountries {
		if !slices.Contains(codes, r.Code) {
			codes = append(codes, r.Code)
		}
	}
	return codes
}

// keepLearningScan stores the result of a scan that asked a tunnel which regions have servers, and those regions as the list
// to mark by: every one of them was scanned unless the scan was cancelled, which leaves only some.
func (s *PsiphonService) keepLearningScan(final psiphon.ScanStatus) {
	if !final.Cancelled {
		codes := make([]string, len(final.Results))
		for i, r := range final.Results {
			codes[i] = r.Region
		}
		s.rememberRegions(codes)
	}
	s.keepScan(final)
}

// CancelScan ends the scan that runs and returns once its tunnels are down. A no-op when none runs.
func (s *PsiphonService) CancelScan() { scans.cancel() }

// ScanStatus is the scan that runs, or the last one this process ran, or after a restart the last one kept.
func (s *PsiphonService) ScanStatus() psiphon.ScanStatus {
	if live := scans.status(); live.StartedAt != 0 {
		return live
	}
	return s.storedScan()
}

// keepScan stores the result of a scan that has ended, so that the page still shows it after the panel restarts.
func (s *PsiphonService) keepScan(final psiphon.ScanStatus) {
	raw, err := json.Marshal(final)
	if err == nil {
		err = s.SetPsiphonRegionScan(string(raw))
	}
	if err != nil {
		logger.Warning("psiphon: cannot keep the result of the scan:", err)
	}
}

// storedScan is the kept result, read as a scan that is not running: a setting is read back, so it is not trusted.
func (s *PsiphonService) storedScan() psiphon.ScanStatus {
	none := psiphon.ScanStatus{Current: []string{}, Results: []psiphon.ScanResult{}}
	raw, err := s.GetPsiphonRegionScan()
	if err != nil || raw == "" {
		return none
	}
	var stored psiphon.ScanStatus
	if json.Unmarshal([]byte(raw), &stored) != nil {
		return none
	}
	stored.Running, stored.Discovering, stored.Current = false, false, []string{}
	stored.Results = slices.DeleteFunc(stored.Results, func(r psiphon.ScanResult) bool { return !psiphon.ValidRegion(r.Region) })
	if stored.Results == nil {
		stored.Results = []psiphon.ScanResult{}
	}
	return stored
}
