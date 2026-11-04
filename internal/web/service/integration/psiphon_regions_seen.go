package integration

import (
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// RegionView is one entry of the picker: a region, and whether Psiphon has servers there. Available is left out while no tunnel
// has told yet, which the picker takes for "any".
type RegionView struct {
	Region
	Available *bool `json:"available,omitempty"`
}

// seenRegions is what the setting keeps of the last list a tunnel reported.
type seenRegions struct {
	Regions []string `json:"regions"`
	SeenAt  int64    `json:"seenAt"`
}

// regionsMu serializes the read, compare and write of that setting, since the picker of every open window asks.
var regionsMu sync.Mutex

// Regions lists the regions of the picker, marking those that Psiphon has servers in. The list comes from the notices of a tunnel
// that is up, and the last one seen is kept in a setting, so that it is there while the tunnels are stopped or starting.
func (s *PsiphonService) Regions() []RegionView {
	known := s.knownRegions()
	out := make([]RegionView, len(isoCountries), len(isoCountries)+len(known))
	for i, r := range isoCountries {
		out[i] = RegionView{Region: r}
	}
	if len(known) == 0 {
		return out
	}
	for i := range out {
		yes := slices.Contains(known, out[i].Code)
		out[i].Available = &yes
	}
	for _, code := range known {
		if !slices.ContainsFunc(isoCountries, func(r Region) bool { return r.Code == code }) {
			yes := true
			out = append(out, RegionView{Region: Region{Code: code, Name: code}, Available: &yes})
		}
	}
	return out
}

// knownRegions is the live list when a tunnel has one, which is then remembered, and else the remembered one.
func (s *PsiphonService) knownRegions() []string {
	regionsMu.Lock()
	defer regionsMu.Unlock()

	stored := s.storedRegions()
	live := psiphon.SeenRegions()
	if len(live.Regions) == 0 {
		return stored.Regions
	}
	if !slices.Equal(live.Regions, stored.Regions) {
		at := live.At
		if at.IsZero() {
			at = time.Now()
		}
		s.saveRegions(seenRegions{Regions: live.Regions, SeenAt: at.Unix()})
	}
	return live.Regions
}

// rememberRegions keeps a list that a scan learned from a tunnel of its own, for the picker to mark the regions by while no tunnel of the
// panel is up. A list with nothing in it is not kept, since it would stand in for the one that is.
func (s *PsiphonService) rememberRegions(codes []string) {
	if len(codes) == 0 {
		return
	}
	regionsMu.Lock()
	defer regionsMu.Unlock()
	s.saveRegions(seenRegions{Regions: codes, SeenAt: time.Now().Unix()})
}

// forgetRegions drops the kept list of regions, so that an install after an uninstall does not mark them by the one before it.
func (s *PsiphonService) forgetRegions() error {
	regionsMu.Lock()
	defer regionsMu.Unlock()
	return s.SetPsiphonRegionsSeen("")
}

func (s *PsiphonService) saveRegions(seen seenRegions) {
	raw, err := json.Marshal(seen)
	if err == nil {
		err = s.SetPsiphonRegionsSeen(string(raw))
	}
	if err != nil {
		logger.Warning("psiphon: cannot keep the list of regions:", err)
	}
}

func (s *PsiphonService) storedRegions() seenRegions {
	raw, err := s.GetPsiphonRegionsSeen()
	if err != nil || raw == "" {
		return seenRegions{}
	}
	var stored seenRegions
	if json.Unmarshal([]byte(raw), &stored) != nil {
		return seenRegions{}
	}
	valid := stored.Regions[:0]
	for _, code := range stored.Regions {
		if psiphon.ValidRegion(code) {
			valid = append(valid, code)
		}
	}
	slices.Sort(valid)
	stored.Regions = slices.Compact(valid)
	return stored
}
