package psiphon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// MaxCountries caps how many country tunnels run beside the default one. Each is a process of its own, about
// 40 MB of memory, and every country of the ISO list at once would be a hundred of them.
const MaxCountries = 6

// The loopback ports the country tunnels draw from: away from the default instance on SocksPort and from Tor on 19050.
const (
	countryPortFirst = 19101
	countryPortLast  = 19199
)

var regionPattern = regexp.MustCompile(`^[A-Z]{2}$`)

// ValidRegion reports whether code can name a country tunnel: two capital letters, since the code is both the
// value of EgressRegion and the name of the directory the tunnel keeps its files in.
func ValidRegion(code string) bool { return regionPattern.MatchString(code) }

// ValidCountryPort reports whether port is one the country tunnels are given.
func ValidCountryPort(port int) bool { return port >= countryPortFirst && port <= countryPortLast }

// Country is one extra tunnel: the region it leaves through and the loopback port its SOCKS proxy serves.
type Country struct {
	Region string `json:"region"`
	Port   int    `json:"port"`
}

func countriesDir() string { return filepath.Join(Dir(), "countries") }

// countryDir is where the tunnel of a region keeps its files; the region must be a valid one.
func countryDir(region string) string { return filepath.Join(countriesDir(), region) }

// spec is where one tunnel-core process keeps its files and which loopback port it serves.
type spec struct {
	config  string
	data    string
	notices string
	port    int
}

// defaultSpec is the instance the panel has always run: the admin's own psiphon.config, on SocksPort.
func defaultSpec() spec {
	return spec{config: ConfigPath(), data: dataDir(), notices: NoticesPath(), port: SocksPort}
}

// countrySpec resolves a country to its files, refusing a region or port that is not one this package hands out:
// the pair comes from a stored setting, and the region becomes a path.
func countrySpec(c Country) (spec, error) {
	if !ValidRegion(c.Region) {
		return spec{}, fmt.Errorf("%q is not a two-letter region code", c.Region)
	}
	if !ValidCountryPort(c.Port) {
		return spec{}, fmt.Errorf("port %d is outside the %d-%d range of the Psiphon countries", c.Port, countryPortFirst, countryPortLast)
	}
	dir := filepath.Join(countriesDir(), c.Region)
	return spec{
		config:  filepath.Join(dir, "psiphon.config"),
		data:    filepath.Join(dir, "data"),
		notices: filepath.Join(dir, "notices.log"),
		port:    c.Port,
	}, nil
}

// PickPort returns the first port of the country range that no other country uses and nothing listens on.
func PickPort(used map[int]bool) (int, error) {
	for port := countryPortFirst; port <= countryPortLast; port++ {
		if !used[port] && portFree(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free port in %d-%d for another Psiphon country", countryPortFirst, countryPortLast)
}

// portFree reports whether nothing holds the loopback port, so a tunnel started on it can bind it.
func portFree(port int) bool {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// renderCountryConfig writes the config a country starts with: the base psiphon.config plus the forced fields,
// its region and its port. It is rewritten at every start, so a new base reaches the country on its next one.
func renderCountryConfig(c Country) error {
	sp, err := countrySpec(c)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		return fmt.Errorf("no base config to start from -- upload a psiphon.config first: %w", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("cannot parse %s: %w", ConfigPath(), err)
	}
	if parsed == nil {
		return fmt.Errorf("cannot parse %s: expected a JSON object", ConfigPath())
	}
	applyForcedFields(parsed)
	parsed["LocalSocksProxyPort"] = c.Port
	parsed["EgressRegion"] = c.Region
	// A legacy data store of the base config would be adopted by every country at once.
	delete(parsed, "MigrateDataStoreDirectory")
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return fmt.Errorf("re-encoding the config of %s: %w", c.Region, err)
	}
	if err := os.MkdirAll(filepath.Dir(sp.config), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(sp.config), err)
	}
	// 0600, like the base config: it carries the same credential.
	return os.WriteFile(sp.config, out, 0o600)
}
