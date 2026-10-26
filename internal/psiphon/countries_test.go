package psiphon

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestValidRegion(t *testing.T) {
	for _, ok := range []string{"DE", "US", "XK"} {
		if !ValidRegion(ok) {
			t.Errorf("ValidRegion(%q) = false, want true", ok)
		}
	}
	// The code names a directory, so anything that is not two capital letters is refused, path tricks included.
	for _, bad := range []string{"", "D", "DEU", "de", "D1", "..", "../", "D/", "D\\", " D", "D "} {
		if ValidRegion(bad) {
			t.Errorf("ValidRegion(%q) = true, want false", bad)
		}
	}
}

func TestCountrySpecRefusesAPairThisPackageDoesNotHandOut(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	for _, c := range []Country{
		{Region: "../x", Port: 19101},
		{Region: "de", Port: 19101},
		{Region: "DE", Port: SocksPort},
		{Region: "DE", Port: 22},
		{Region: "DE", Port: 19200},
		{Region: "DE", Port: 0},
	} {
		if _, err := countrySpec(c); err == nil {
			t.Errorf("countrySpec(%+v) was accepted", c)
		}
	}
	sp, err := countrySpec(Country{Region: "DE", Port: 19101})
	if err != nil {
		t.Fatalf("countrySpec: %v", err)
	}
	dir := filepath.Join(countriesDir(), "DE")
	if sp.config != filepath.Join(dir, "psiphon.config") || sp.data != filepath.Join(dir, "data") || sp.notices != filepath.Join(dir, "notices.log") || sp.port != 19101 {
		t.Fatalf("spec = %+v, want the files of the region under %s", sp, dir)
	}
}

func holdPort(t *testing.T, port int) {
	t.Helper()
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("port %d is not free on this host: %v", port, err)
	}
	t.Cleanup(func() { _ = listener.Close() })
}

func TestPickPortSkipsUsedAndBusyPorts(t *testing.T) {
	holdPort(t, countryPortFirst)
	got, err := PickPort(map[int]bool{countryPortFirst + 1: true})
	if err != nil {
		t.Fatalf("PickPort: %v", err)
	}
	if got != countryPortFirst+2 {
		t.Fatalf("PickPort = %d, want %d: the first is held, the second is taken by another country", got, countryPortFirst+2)
	}
}

func TestPickPortFailsWhenTheRangeIsFull(t *testing.T) {
	used := map[int]bool{}
	for port := countryPortFirst; port <= countryPortLast; port++ {
		used[port] = true
	}
	if _, err := PickPort(used); err == nil {
		t.Fatal("PickPort found a port in a full range")
	}
}

func TestRenderCountryConfigForcesTheFieldsOfTheCountry(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	// A base config as an admin may paste it: the Docker deployment's, with its listen-anywhere and its own port.
	base := `{
  "PropagationChannelId": "kept-untouched",
  "SponsorId": "kept-too",
  "EgressRegion": "IT",
  "ListenInterface": "any",
  "LocalSocksProxyPort": 1080,
  "DisableLocalSocksProxy": true,
  "DisableLocalHTTPProxy": false,
  "MigrateDataStoreDirectory": "/config"
}`
	if err := SaveConfig([]byte(base)); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if err := renderCountryConfig(Country{Region: "DE", Port: 19101}); err != nil {
		t.Fatalf("renderCountryConfig: %v", err)
	}
	sp, err := countrySpec(Country{Region: "DE", Port: 19101})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(sp.config)
	if err != nil {
		t.Fatalf("read the rendered config: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("rendered config is not JSON: %v", err)
	}
	want := map[string]any{
		"EgressRegion":           "DE",
		"LocalSocksProxyPort":    float64(19101),
		"DisableLocalSocksProxy": false,
		"DisableLocalHTTPProxy":  true,
		"ListenInterface":        "",
		"PropagationChannelId":   "kept-untouched",
		"SponsorId":              "kept-too",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	if _, present := got["MigrateDataStoreDirectory"]; present {
		t.Error("the legacy data store of the base config was passed on: every country would adopt it")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(sp.config); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("config mode = %v (%v), want 0600: it carries the same credential as the base", info, err)
		}
	}
	// The base itself is what the admin uploaded, with the forced fields only.
	baseOnDisk, err := os.ReadFile(ConfigPath())
	if err != nil || strings.Contains(string(baseOnDisk), `"DE"`) {
		t.Fatalf("the base config was touched by a country (%v): %s", err, baseOnDisk)
	}
}

func TestRenderCountryConfigNeedsABaseConfig(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	err := renderCountryConfig(Country{Region: "DE", Port: 19101})
	if err == nil || !strings.Contains(err.Error(), "upload") {
		t.Fatalf("renderCountryConfig without a base = %v, want the advice to upload a psiphon.config", err)
	}
}
