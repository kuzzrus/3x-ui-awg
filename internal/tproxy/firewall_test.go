package tproxy

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderFirewallRulesetDedupesAndSorts(t *testing.T) {
	got := renderFirewallRuleset([]int{2398, 8888, 2398, 43210})
	if !strings.Contains(got, "{ 2398, 8888, 43210 }") {
		t.Errorf("ruleset = %q, want a sorted, deduplicated port set", got)
	}
	if !strings.Contains(got, "add table inet "+firewallTable) {
		t.Errorf("ruleset = %q, want it to declare table %q", got, firewallTable)
	}
	if !strings.Contains(got, `iifname != "lo"`) {
		t.Errorf("ruleset = %q, must restrict to non-loopback interfaces only", got)
	}
	if !strings.Contains(got, "drop") {
		t.Errorf("ruleset = %q, must drop matching traffic", got)
	}
	if !strings.Contains(got, "ct state established,related accept") {
		t.Errorf("ruleset = %q, must accept established/related traffic so a recycled ephemeral port cannot black-hole this host's own outbound connections", got)
	}
}

func TestRenderFirewallRulesetSingleValue(t *testing.T) {
	got := renderFirewallRuleset([]int{2398})
	if !strings.Contains(got, "{ 2398 }") {
		t.Errorf("ruleset = %q, want a single-element set", got)
	}
}

// Regression: this chain's rules must never be replaced via "flush table"
// (which would also wipe the sibling egress_redirect chain applyEgressRedirects
// owns in the same table) -- only "flush chain ... local_backend", scoped to
// this chain alone. See renderFirewallRuleset's own doc comment for why
// add table/add chain make this safe to run unconditionally, with no
// "does it already exist" branch needed.
func TestRenderFirewallRulesetFlushesOnlyItsOwnChain(t *testing.T) {
	got := renderFirewallRuleset([]int{2398})
	if strings.Contains(got, "flush table") {
		t.Errorf("ruleset = %q, must never flush the whole table -- that would also wipe egress_redirect", got)
	}
	if !strings.Contains(got, "flush chain inet "+firewallTable+" local_backend") {
		t.Errorf("ruleset = %q, want it to flush exactly its own chain", got)
	}
}

func TestRenderEgressRedirectRulesetShape(t *testing.T) {
	got := renderEgressRedirectRuleset([]egressRedirect{{GID: 2000000007, ToPort: 12345}})
	if strings.Contains(got, "flush table") {
		t.Errorf("ruleset = %q, must never flush the whole table -- that would also wipe local_backend", got)
	}
	if !strings.Contains(got, "flush chain inet "+firewallTable+" egress_redirect") {
		t.Errorf("ruleset = %q, want it to flush exactly its own chain", got)
	}
	if !strings.Contains(got, "meta skgid 2000000007 meta l4proto tcp redirect to :12345") {
		t.Errorf("ruleset = %q, want the exact rule shape verified against real nft syntax (bare \"tcp\" is a syntax error in this position, and matching must be by gid so distinct inbounds' engines -- which all share one uid -- stay isolated from each other)", got)
	}
}

func TestRenderEgressRedirectRulesetSortsDeterministically(t *testing.T) {
	a := renderEgressRedirectRuleset([]egressRedirect{{GID: 1, ToPort: 200}, {GID: 1, ToPort: 100}})
	b := renderEgressRedirectRuleset([]egressRedirect{{GID: 1, ToPort: 100}, {GID: 1, ToPort: 200}})
	if a != b {
		t.Errorf("renderEgressRedirectRuleset must sort its input, got two different outputs for the same set:\n%q\n%q", a, b)
	}
}

// Regression for the shared-UID isolation bug: two different inbounds' own
// engines share the same UID (nobody) but must get distinct GID-scoped
// rules, so enabling routeThroughXray on one can never also redirect the
// other's traffic.
func TestRenderEgressRedirectRulesetDistinguishesByGID(t *testing.T) {
	got := renderEgressRedirectRuleset([]egressRedirect{{GID: mtproxyEgressGID(7), ToPort: 30001}, {GID: mtproxyEgressGID(9), ToPort: 30002}})
	if !strings.Contains(got, fmt.Sprintf("meta skgid %d meta l4proto tcp redirect to :30001", mtproxyEgressGID(7))) {
		t.Errorf("ruleset = %q, want a distinct rule for inbound 7's own gid", got)
	}
	if !strings.Contains(got, fmt.Sprintf("meta skgid %d meta l4proto tcp redirect to :30002", mtproxyEgressGID(9))) {
		t.Errorf("ruleset = %q, want a distinct rule for inbound 9's own gid", got)
	}
}

func TestRenderEgressRedirectRulesetEmptyStillDeclaresChain(t *testing.T) {
	got := renderEgressRedirectRuleset(nil)
	if !strings.Contains(got, "add chain inet "+firewallTable+" egress_redirect") {
		t.Errorf("ruleset = %q, an empty redirect set must still declare the (now-empty) chain, not skip it", got)
	}
	if strings.Contains(got, "redirect to") {
		t.Errorf("ruleset = %q, want no redirect rules when there are none", got)
	}
}
