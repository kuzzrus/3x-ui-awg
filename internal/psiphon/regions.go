package psiphon

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// RegionsSeen is the list of egress regions a tunnel-core said it has servers for, and when it said so.
type RegionsSeen struct {
	Regions []string
	At      time.Time
}

// SeenRegions is the newest list of regions that a tunnel of this panel reported, from the notice logs of the default tunnel and
// of the countries. It is empty while none has started since its log was cleared, which every start does.
func SeenRegions() RegionsSeen {
	paths := []string{NoticesPath()}
	if entries, err := os.ReadDir(countriesDir()); err == nil {
		for _, e := range entries {
			if e.IsDir() && ValidRegion(e.Name()) {
				paths = append(paths, filepath.Join(countryDir(e.Name()), "notices.log"))
			}
		}
	}
	var best RegionsSeen
	for _, path := range paths {
		seen, err := seenRegionsIn(path)
		if err != nil || len(seen.Regions) == 0 {
			continue
		}
		if len(best.Regions) == 0 || seen.At.After(best.At) {
			best = seen
		}
	}
	return best
}

// seenRegionsIn reads the last AvailableEgressRegions notice of the log at path: the regions the process has servers for. The notice
// is sent when the list of servers is loaded, and a newer one may follow, so the last is the one that stands.
func seenRegionsIn(path string) (RegionsSeen, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RegionsSeen{}, nil
		}
		return RegionsSeen{}, err
	}
	defer f.Close()

	var seen RegionsSeen
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	for scanner.Scan() {
		var n notice
		if json.Unmarshal(scanner.Bytes(), &n) != nil || n.NoticeType != "AvailableEgressRegions" {
			continue
		}
		var d struct {
			Regions []string `json:"regions"`
		}
		if json.Unmarshal(n.Data, &d) != nil {
			continue
		}
		seen = RegionsSeen{Regions: cleanRegions(d.Regions)}
		if at, err := time.Parse(time.RFC3339Nano, n.Timestamp); err == nil {
			seen.At = at
		}
	}
	return seen, scanner.Err()
}

// cleanRegions keeps the codes this package would hand out, once each and in order: a log is read back, so what is in it is not trusted.
func cleanRegions(codes []string) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		if ValidRegion(c) {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}
