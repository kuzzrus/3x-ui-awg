package psiphon

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func writeLog(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func regionsNotice(timestamp string, regions ...string) string {
	quoted := make([]string, len(regions))
	for i, r := range regions {
		quoted[i] = `"` + r + `"`
	}
	return `{"data":{"regions":[` + strings.Join(quoted, ",") + `]},"noticeType":"AvailableEgressRegions","timestamp":"` + timestamp + `"}`
}

func TestSeenRegionsInTakesTheLastListAndTrustsNothingInIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notices.log")
	writeLog(t, path,
		regionsNotice("2026-10-27T09:00:00.000Z", "DE", "US"),
		`{"data":{"count":1},"noticeType":"Tunnels","timestamp":"2026-10-27T09:00:05.000Z"}`,
		`not json at all`,
		regionsNotice("2026-10-27T09:41:45.362Z", "US", "de", "../x", "JP", "DE", "DE", "AT", "USA"),
	)

	got, err := seenRegionsIn(path)
	if err != nil {
		t.Fatalf("seenRegionsIn: %v", err)
	}
	if want := []string{"AT", "DE", "JP", "US"}; !slices.Equal(got.Regions, want) {
		t.Fatalf("regions = %v, want %v: the last list, once each, in order, and only codes this package would hand out", got.Regions, want)
	}
	if want := time.Date(2026, 10, 27, 9, 41, 45, 362_000_000, time.UTC); !got.At.Equal(want) {
		t.Fatalf("at = %v, want the timestamp of that notice, %v", got.At, want)
	}
}

func TestSeenRegionsInIsEmptyWithoutALogOrAList(t *testing.T) {
	dir := t.TempDir()
	if got, err := seenRegionsIn(filepath.Join(dir, "absent.log")); err != nil || len(got.Regions) != 0 {
		t.Fatalf("an absent log = %+v, %v, want nothing", got, err)
	}
	path := filepath.Join(dir, "notices.log")
	writeLog(t, path, `{"data":{"count":0},"noticeType":"Tunnels","timestamp":"2026-10-27T09:00:05.000Z"}`)
	if got, err := seenRegionsIn(path); err != nil || len(got.Regions) != 0 {
		t.Fatalf("a log without the notice = %+v, %v, want nothing", got, err)
	}
}

func TestSeenRegionsPrefersTheNewestListOfTheDefaultTunnelAndTheCountries(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	writeLog(t, NoticesPath(), regionsNotice("2026-10-27T09:00:00.000Z", "DE", "US"))
	writeLog(t, filepath.Join(countryDir("FR"), "notices.log"), regionsNotice("2026-10-27T10:00:00.000Z", "DE", "FR", "US"))
	writeLog(t, filepath.Join(countryDir("JP"), "notices.log"), regionsNotice("2026-10-27T08:00:00.000Z", "JP"))
	if err := os.MkdirAll(filepath.Join(countriesDir(), "..bad"), 0o700); err != nil {
		t.Fatal(err)
	}

	got := SeenRegions()

	if want := []string{"DE", "FR", "US"}; !slices.Equal(got.Regions, want) {
		t.Fatalf("regions = %v, want the list of the tunnel that spoke last, %v", got.Regions, want)
	}
}

func TestSeenRegionsIsEmptyBeforeAnyTunnelHasSpoken(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if got := SeenRegions(); len(got.Regions) != 0 {
		t.Fatalf("regions = %v, want none", got.Regions)
	}
}
