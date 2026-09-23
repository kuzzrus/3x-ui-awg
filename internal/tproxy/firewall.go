package tproxy

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// firewallTable is a dedicated nftables table this package owns exclusively,
// so applying or removing it never touches an admin's own rules.
const firewallTable = "tproxy_backend"

// firewallTimeout bounds every nft invocation -- Manager holds its own lock
// for the duration of each call.
const firewallTimeout = 5 * time.Second

// ensureFirewall blocks non-loopback access to the given MTProxy client
// ports (MTProxy's -H binds every interface); apply before the process
// starts. Touches only the local_backend chain -- see applyEgressRedirects
// for the sibling egress_redirect chain this same table also owns, applied
// and gated completely independently (renderFirewallRuleset's doc comment
// explains why one can never disturb the other).
func ensureFirewall(ctx context.Context, ports []int) error {
	return applyFirewall(ctx, ports)
}

// removeFirewall deletes the whole table this package owns, both chains
// included. Only called when there are zero mtproxy ports anywhere
// (recomputeSharedServerLocked's "no clients on any inbound" branch) -- the
// only state where egress_redirect could still matter is a running engine,
// and zero ports means zero running engines, so there is nothing left to
// redirect either. Best-effort and idempotent -- an absent table is the
// common case, not an error.
func removeFirewall(ctx context.Context) {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, firewallTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, nft, "delete", "table", "inet", firewallTable).Run()
}

// applyFirewall is indirected so tests can replace the real nft invocation --
// exercising the Manager's own logic needs neither a real nft binary nor root.
var applyFirewall = applyFirewallViaNft

// applyFirewallViaNft declares the table if missing and replaces only the
// local_backend chain's rules.
func applyFirewallViaNft(ctx context.Context, ports []int) error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf("nft (nftables) is required to safely expose an MTProxy backend port and was not found in PATH: %w", err)
	}
	if len(ports) == 0 {
		removeFirewall(ctx)
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, firewallTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, nft, "-f", "-")
	cmd.Stdin = strings.NewReader(renderFirewallRuleset(ports))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft -f: %w: %s", err, string(out))
	}
	return nil
}

// renderFirewallRuleset builds the local_backend chain's nft(8) script text,
// deduplicated and sorted. Uses "add table"/"add chain" (idempotent no-ops
// when already present with the same spec) plus "flush chain" rather than
// "flush table", so this can never disturb the sibling egress_redirect
// chain applyEgressRedirectsViaNft owns in the same table -- verified
// empirically: reapplying one chain's rules this way leaves a sibling
// chain in the same table completely untouched, in either direction.
func renderFirewallRuleset(ports []int) string {
	unique := make(map[int]struct{}, len(ports))
	for _, p := range ports {
		unique[p] = struct{}{}
	}
	sorted := make([]int, 0, len(unique))
	for p := range unique {
		sorted = append(sorted, p)
	}
	sort.Ints(sorted)

	strs := make([]string, len(sorted))
	for i, p := range sorted {
		strs[i] = strconv.Itoa(p)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", firewallTable)
	fmt.Fprintf(&b, "add chain inet %s local_backend { type filter hook input priority -10 ; policy accept ; }\n", firewallTable)
	fmt.Fprintf(&b, "flush chain inet %s local_backend\n", firewallTable)
	// A recycled ephemeral port must not drop this host's own outbound replies.
	fmt.Fprintf(&b, "add rule inet %s local_backend ct state established,related accept\n", firewallTable)
	fmt.Fprintf(&b, "add rule inet %s local_backend iifname != \"lo\" tcp dport { %s } drop\n", firewallTable, strings.Join(strs, ", "))
	return b.String()
}

// egressRedirect is one routeThroughXray inbound's transparent-redirect
// rule: every outbound TCP connection made by the process running as GID is
// rewritten to 127.0.0.1:ToPort, where a dokodemo-door Xray inbound
// (injectTproxyEgress, internal/web/service/xray.go) recovers the real
// destination via SO_ORIGINAL_DST and hands the connection to Xray's own
// router. The MTProxy engine binary itself has no proxy/SOCKS capability at
// all (confirmed by grepping TelegramMessenger/MTProxy's own source), unlike
// mtg (internal/mtproto's sidecar) -- this OS-level redirect is the only way
// to route its traffic through Xray, since the engine can't be told to.
//
// GID, not UID: every MTProxy engine child runs as the same UID (nobody)
// regardless of inbound (privdrop.go's mtproxyUser), so a uid-based match
// can only ever mean "every tproxy engine on this box" -- live-confirmed as
// a real bug, not a hypothetical, before this field was GID: enabling
// routeThroughXray on one test inbound silently redirected a second,
// non-routed inbound's own engine too, since nftables has no notion of
// "inbound," only process credentials. mtproxyEgressGID (privdrop.go) gives
// each inbound's engine child a distinct process GID for exactly this
// match to key on; see that function's doc comment for the live PoC that
// confirmed skgid correctly separates two children sharing one UID.
//
// Live-verified (then fully removed, no residue) on a real test box: exactly
// this rule, "meta skgid <gid> meta l4proto tcp redirect to :<port>" in a
// "type nat hook output" chain, correctly redirects a specific gid's own
// outbound TCP while preserving the true destination via SO_ORIGINAL_DST,
// and leaves a sibling process sharing the same UID but a different GID
// alone. Bare "tcp" instead of "meta l4proto tcp" is a syntax error in this
// position ("transport protocol mapping is only valid after transport
// protocol match") -- confirmed empirically twice, don't simplify it away.
type egressRedirect struct {
	GID    uint32
	ToPort int
}

// applyEgressRedirects is indirected for the same reason as applyFirewall.
var applyEgressRedirects = applyEgressRedirectsViaNft

// applyEgressRedirectsViaNft is applyFirewallViaNft's sibling for the
// egress_redirect chain -- same table, applied completely independently, so
// a call here never touches local_backend and vice versa. An empty
// redirects list still declares the (now-empty) chain rather than deleting
// anything: local_backend may still need the table, and an empty chain with
// policy accept is simply inert, not an error state.
func applyEgressRedirectsViaNft(ctx context.Context, redirects []egressRedirect) error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf("nft (nftables) is required to route tproxy traffic through Xray and was not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, firewallTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, nft, "-f", "-")
	cmd.Stdin = strings.NewReader(renderEgressRedirectRuleset(redirects))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft -f: %w: %s", err, string(out))
	}
	return nil
}

// renderEgressRedirectRuleset builds the egress_redirect chain's script
// text, sorted for deterministic output.
func renderEgressRedirectRuleset(redirects []egressRedirect) string {
	sorted := append([]egressRedirect(nil), redirects...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].GID != sorted[j].GID {
			return sorted[i].GID < sorted[j].GID
		}
		return sorted[i].ToPort < sorted[j].ToPort
	})

	var b strings.Builder
	fmt.Fprintf(&b, "add table inet %s\n", firewallTable)
	fmt.Fprintf(&b, "add chain inet %s egress_redirect { type nat hook output priority -100 ; policy accept ; }\n", firewallTable)
	fmt.Fprintf(&b, "flush chain inet %s egress_redirect\n", firewallTable)
	for _, r := range sorted {
		fmt.Fprintf(&b, "add rule inet %s egress_redirect meta skgid %d meta l4proto tcp redirect to :%d\n", firewallTable, r.GID, r.ToPort)
	}
	return b.String()
}
