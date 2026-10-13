package adguard

import (
	"os"
	"strings"
	"testing"
)

func TestSetPortsMovesBothPortsAndLeavesTheRestOfTheConfigAlone(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if _, err := Seed(SeedOptions{WebPort: 3000, DNSPort: 5335}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := SetPorts(41000, 41001); err != nil {
		t.Fatal(err)
	}
	if web, err := WebPort(); err != nil || web != 41000 {
		t.Errorf("web port %d, %v, want 41000", web, err)
	}
	if dns, err := DNSPort(); err != nil || dns != 41001 {
		t.Errorf("DNS port %d, %v, want 41001", dns, err)
	}
	after, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	was, now := strings.Split(string(before), "\n"), strings.Split(string(after), "\n")
	if len(was) != len(now) {
		t.Fatalf("the config had %d lines and has %d", len(was), len(now))
	}
	var changed []string
	for i := range was {
		if was[i] != now[i] {
			changed = append(changed, strings.TrimSpace(was[i])+" -> "+strings.TrimSpace(now[i]))
		}
	}
	if len(changed) != 2 {
		t.Errorf("%d lines changed, want the two of the ports: %q", len(changed), changed)
	}
}

// AdGuard Home writes its config out in full, in its own order and with its own comments: only the two ports may move,
// and not the other settings that are named port.
func TestReplacePortsMovesNothingButTheTwoOfTheConfig(t *testing.T) {
	raw := `# written by AdGuard Home
http:
  pprof:
    port: 6060
    enabled: false
  address: 127.0.0.1:3000
# the resolver
dns:
  bind_hosts:
    - 127.0.0.1
  port: 5335
  upstream_dns:
    - https://dns10.quad9.net/dns-query
users:
  - name: admin
    password: $2a$10$abc
`
	out, err := replacePorts([]byte(raw), 41000, 41001)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.NewReplacer(
		"address: 127.0.0.1:3000", `address: "127.0.0.1:41000"`,
		"  port: 5335", "  port: 41001",
	).Replace(raw)
	if string(out) != want {
		t.Errorf("the config came out as\n%s\nwant\n%s", out, want)
	}
}

func TestReplacePortsSaysWhichPortTheConfigHasNoPlaceFor(t *testing.T) {
	for name, tc := range map[string]struct{ raw, mention string }{
		"no web address": {"dns:\n  port: 5335\n", "http.address"},
		"no DNS port":    {"http:\n  address: 127.0.0.1:3000\n", "dns.port"},
		"no YAML at all": {"http: [unclosed\n", "cannot parse"},
	} {
		if _, err := replacePorts([]byte(tc.raw), 41000, 41001); err == nil || !strings.Contains(err.Error(), tc.mention) {
			t.Errorf("%s: error %v, want one that mentions %q", name, err, tc.mention)
		}
	}
}

func TestSetPortsRefusesAPortThatIsNoPortAndLeavesTheConfig(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if _, err := Seed(SeedOptions{WebPort: 3000, DNSPort: 5335}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(ConfigPath())
	for _, ports := range [][2]int{{0, 5335}, {3000, 0}, {70000, 5335}, {3000, -1}} {
		if err := SetPorts(ports[0], ports[1]); err == nil {
			t.Errorf("ports %v were taken", ports)
		}
	}
	if after, _ := os.ReadFile(ConfigPath()); string(after) != string(before) {
		t.Error("a refused change altered the config")
	}
}
