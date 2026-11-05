package integration

import (
	"cmp"
	"encoding/json"
	"slices"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// connectedMu serializes the read, merge and write of the regions that connected in a scan, since a scan ends while a page asks.
var connectedMu sync.Mutex

// rememberConnected adds the regions that connected in a scan to those kept from the scans before it, each with its newest success;
// a region that does not connect in a later scan stays as it was.
func (s *PsiphonService) rememberConnected(final psiphon.ScanStatus) {
	connectedMu.Lock()
	defer connectedMu.Unlock()
	kept := s.connectedBefore()
	changed := false
	for _, r := range final.Results {
		if !r.Connected {
			continue
		}
		seen := psiphon.ConnectedRegion{Region: r.Region, At: final.FinishedAt, ConnectMs: r.ConnectMs, LatencyMs: r.LatencyMs, IP: r.IP, Country: r.Country}
		if i := slices.IndexFunc(kept, func(k psiphon.ConnectedRegion) bool { return k.Region == r.Region }); i >= 0 {
			kept[i] = seen
		} else {
			kept = append(kept, seen)
		}
		changed = true
	}
	if !changed {
		return
	}
	raw, err := json.Marshal(kept)
	if err == nil {
		err = s.SetPsiphonRegionsConnected(string(raw))
	}
	if err != nil {
		logger.Warning("psiphon: cannot keep the regions that connected in the scan:", err)
	}
}

// connectedBefore is the kept list, one entry per region and the newest, in the order of the regions: a setting is read back, so it
// is not trusted.
func (s *PsiphonService) connectedBefore() []psiphon.ConnectedRegion {
	none := []psiphon.ConnectedRegion{}
	raw, err := s.GetPsiphonRegionsConnected()
	if err != nil || raw == "" {
		return none
	}
	var kept []psiphon.ConnectedRegion
	if json.Unmarshal([]byte(raw), &kept) != nil {
		return none
	}
	kept = slices.DeleteFunc(kept, func(k psiphon.ConnectedRegion) bool { return !psiphon.ValidRegion(k.Region) || k.At <= 0 })
	slices.SortStableFunc(kept, func(a, b psiphon.ConnectedRegion) int {
		return cmp.Or(cmp.Compare(a.Region, b.Region), cmp.Compare(b.At, a.At))
	})
	kept = slices.CompactFunc(kept, func(a, b psiphon.ConnectedRegion) bool { return a.Region == b.Region })
	if kept == nil {
		return none
	}
	return kept
}

// forgetConnected drops the regions kept from earlier scans, so that an install after an uninstall does not show those before it.
func (s *PsiphonService) forgetConnected() error {
	connectedMu.Lock()
	defer connectedMu.Unlock()
	return s.SetPsiphonRegionsConnected("")
}
