package portguard

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// table is the nftables table this package owns, so that its rules are replaced or removed without touching the admin's own.
const table = "x_ui_portguard"

// nftTimeout bounds every call of nft.
const nftTimeout = 5 * time.Second

// renderRuleset is the nft script that closes ports, or the one that removes the rules when there are none. A closed port drops
// what a remote host begins towards it, and the packets of a connection it already has, which is what closing a port means.
func renderRuleset(ports []Port) string {
	if len(ports) == 0 {
		return fmt.Sprintf("delete table inet %s\n", table)
	}
	var b strings.Builder
	// The table is flushed, not deleted, so that nft takes the whole change in one transaction.
	fmt.Fprintf(&b, "add table inet %s\n", table)
	fmt.Fprintf(&b, "flush table inet %s\n", table)
	fmt.Fprintf(&b, "add chain inet %s input { type filter hook input priority -10 ; policy accept ; }\n", table)
	// This host's own traffic and its VPN interfaces are never closed out. Replies to its own connections are not matched: they
	// belong to the reply direction of their entry, whatever port they come back to.
	for _, iface := range []string{"lo", "awg*", "wg*"} {
		fmt.Fprintf(&b, "add rule inet %s input iifname %q accept\n", table, iface)
	}
	for _, proto := range []Proto{TCP, UDP} {
		var numbers []string
		for _, p := range ports {
			if p.Proto == proto {
				numbers = append(numbers, strconv.Itoa(p.Number))
			}
		}
		if len(numbers) > 0 {
			fmt.Fprintf(&b, "add rule inet %s input ct direction original %s dport { %s } drop\n", table, proto, strings.Join(numbers, ", "))
		}
	}
	return b.String()
}

// NftFirewall is the Firewall of the host: nft(8).
type NftFirewall struct{}

// Available reports whether nft can be run at all.
func (NftFirewall) Available() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

// Apply replaces the rules with the ones that close ports.
func (NftFirewall) Apply(ctx context.Context, ports []Port) error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return fmt.Errorf("portguard: nft (nftables) was not found in PATH: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, nft, "-f", "-")
	cmd.Stdin = strings.NewReader(renderRuleset(ports))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("portguard: nft -f: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Remove deletes the table; a table that is not there is what a host usually has, so it is not an error.
func (NftFirewall) Remove(ctx context.Context) error {
	nft, err := exec.LookPath("nft")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, nft, "delete", "table", "inet", table).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "No such file or directory") {
		return fmt.Errorf("portguard: nft delete table: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
