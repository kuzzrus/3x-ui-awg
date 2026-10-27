package integration

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/psiphon"
)

// tunnelSaysRegions writes the notice a tunnel-core sends when it has loaded its list of servers.
func tunnelSaysRegions(t *testing.T, timestamp string, regions ...string) {
	t.Helper()
	list := ""
	for i, r := range regions {
		if i > 0 {
			list += ","
		}
		list += `"` + r + `"`
	}
	line := `{"data":{"regions":[` + list + `]},"noticeType":"AvailableEgressRegions","timestamp":"` + timestamp + `"}` + "\n"
	if err := os.MkdirAll(filepath.Dir(psiphon.NoticesPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(psiphon.NoticesPath(), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func availability(regions []RegionView) map[string]string {
	out := map[string]string{}
	for _, r := range regions {
		switch {
		case r.Available == nil:
			out[r.Code] = "unknown"
		case *r.Available:
			out[r.Code] = "yes"
		default:
			out[r.Code] = "no"
		}
	}
	return out
}

func TestRegionsAreAnyUntilATunnelHasSaidWhichOnesHaveServers(t *testing.T) {
	useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	regions := (&PsiphonService{}).Regions()

	if len(regions) != len(isoCountries) {
		t.Fatalf("%d regions, want the whole list of %d", len(regions), len(isoCountries))
	}
	for _, r := range regions {
		if r.Available != nil {
			t.Fatalf("%s is marked %v before any tunnel said anything, want it left unmarked", r.Code, *r.Available)
		}
	}
}

func TestRegionsMarkTheOnesWithServersAndKeepTheListForWhenNoTunnelIsUp(t *testing.T) {
	useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	svc := &PsiphonService{}

	tunnelSaysRegions(t, "2026-10-27T09:41:45.362Z", "DE", "US", "ZZ")
	got := availability(svc.Regions())
	if got["DE"] != "yes" || got["US"] != "yes" || got["FR"] != "no" || got["JP"] != "no" {
		t.Fatalf("with a list of DE, US and ZZ: DE %s, US %s, FR %s, JP %s", got["DE"], got["US"], got["FR"], got["JP"])
	}
	if got["ZZ"] != "yes" {
		t.Fatalf("a region that the list of countries lacks is %s, want it offered, since a tunnel has servers there", got["ZZ"])
	}

	// The tunnel stops, and the log of its next start is made afresh: the list stands until another one is said.
	if err := os.Remove(psiphon.NoticesPath()); err != nil {
		t.Fatal(err)
	}
	got = availability(svc.Regions())
	if got["DE"] != "yes" || got["FR"] != "no" {
		t.Fatalf("with no tunnel up: DE %s, FR %s, want the last list kept", got["DE"], got["FR"])
	}

	tunnelSaysRegions(t, "2026-10-28T09:00:00.000Z", "FR")
	got = availability(svc.Regions())
	if got["FR"] != "yes" || got["DE"] != "no" {
		t.Fatalf("with a new list of FR: FR %s, DE %s, want the new list to replace the old", got["FR"], got["DE"])
	}
}

func TestRegionsTrustNothingInTheKeptList(t *testing.T) {
	useTunnels(t, nil)
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	svc := &PsiphonService{}

	if err := svc.SetPsiphonRegionsSeen(`{"regions":["US","../x","de","DE","DE"],"seenAt":1}`); err != nil {
		t.Fatal(err)
	}
	regions := svc.Regions()
	if len(regions) != len(isoCountries) {
		t.Fatalf("%d regions, want the %d of the list and nothing of the entries that are no region", len(regions), len(isoCountries))
	}
	if got := availability(regions); got["DE"] != "yes" || got["US"] != "yes" || got["FR"] != "no" {
		t.Fatalf("DE %s, US %s, FR %s, want the valid codes of the kept list taken once each", got["DE"], got["US"], got["FR"])
	}

	if err := svc.SetPsiphonRegionsSeen(`{broken`); err != nil {
		t.Fatal(err)
	}
	for _, r := range svc.Regions() {
		if r.Available != nil {
			t.Fatalf("%s is marked after a kept list that cannot be read, want nothing marked", r.Code)
		}
	}
}
