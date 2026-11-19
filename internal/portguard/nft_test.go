package portguard

import (
	"strings"
	"testing"
)

func TestRenderRulesetClosesThePortsOfBothTransports(t *testing.T) {
	got := renderRuleset([]Port{{2053, TCP}, {8443, TCP}, {51820, UDP}, {443, UDP}})
	want := strings.Join([]string{
		"add table inet x_ui_portguard",
		"flush table inet x_ui_portguard",
		"add chain inet x_ui_portguard input { type filter hook input priority -10 ; policy accept ; }",
		`add rule inet x_ui_portguard input iifname "lo" accept`,
		`add rule inet x_ui_portguard input iifname "awg*" accept`,
		`add rule inet x_ui_portguard input iifname "wg*" accept`,
		"add rule inet x_ui_portguard input ct direction original tcp dport { 2053, 8443 } drop",
		"add rule inet x_ui_portguard input ct direction original udp dport { 51820, 443 } drop",
		"",
	}, "\n")
	if got != want {
		t.Errorf("renderRuleset =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderRulesetLeavesOutATransportWithNoPorts(t *testing.T) {
	onlyTCP := renderRuleset(ports(TCP, 2053))
	if !strings.Contains(onlyTCP, "tcp dport { 2053 } drop") || strings.Contains(onlyTCP, "udp dport") {
		t.Errorf("a TCP-only ruleset =\n%s", onlyTCP)
	}
	onlyUDP := renderRuleset(ports(UDP, 51820))
	if !strings.Contains(onlyUDP, "udp dport { 51820 } drop") || strings.Contains(onlyUDP, "tcp dport") {
		t.Errorf("a UDP-only ruleset =\n%s", onlyUDP)
	}
}

func TestRenderRulesetOfNoPortsRemovesTheTable(t *testing.T) {
	if got, want := renderRuleset(nil), "delete table inet x_ui_portguard\n"; got != want {
		t.Errorf("renderRuleset(nil) = %q, want %q", got, want)
	}
}

// The accepts come first, so that nothing local or inside a tunnel is ever dropped by a port rule behind them.
func TestRenderRulesetAcceptsLocalAndTunnelTrafficBeforeItDrops(t *testing.T) {
	got := renderRuleset(ports(TCP, 2053))
	drop := strings.Index(got, "drop")
	for _, iface := range []string{`"lo"`, `"awg*"`, `"wg*"`} {
		i := strings.Index(got, "iifname "+iface+" accept")
		if i < 0 {
			t.Fatalf("no accept for %s in\n%s", iface, got)
		}
		if i > drop {
			t.Errorf("the accept for %s comes after the drop", iface)
		}
	}
}

// Only what a remote host begins is dropped: replies to this host's own connections come back to ports of their own.
func TestRenderRulesetDropsOnlyTheOriginalDirection(t *testing.T) {
	for _, line := range strings.Split(renderRuleset([]Port{{2053, TCP}, {51820, UDP}}), "\n") {
		if strings.HasSuffix(line, "drop") && !strings.Contains(line, "ct direction original") {
			t.Errorf("a drop that is not limited to the original direction: %s", line)
		}
	}
}
