package portguard

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/hostfw"
)

// table is the nftables table this package owns, so that its rules are replaced or removed without touching the admin's own.
const table = "x_ui_portguard"

// TableName is that table's name, for a caller that has to leave it out of what it reads of the host's firewall.
const TableName = table

// nftTimeout bounds every call of nft.
const nftTimeout = 5 * time.Second

// SetNftSearchDirs replaces the directories searched after PATH, so that a test does not find the nft of the host it runs on;
// the returned func puts them back. They are the ones that hostfw looks for the tools of the firewall in, nft among them.
func SetNftSearchDirs(dirs []string) (restore func()) { return hostfw.SetSearchDirs(dirs) }

// findNft is the path of nft, from PATH or else from the usual places for it, found where hostfw finds it.
func findNft() (string, error) { return hostfw.LookPath("nft") }

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

// Installed reports whether nft can be found at all.
func (NftFirewall) Installed() bool {
	_, err := findNft()
	return err == nil
}

// Available reports whether nft is there and may read the rules, which it may not in a container without NET_ADMIN.
func (NftFirewall) Available() bool {
	nft, err := findNft()
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), nftTimeout)
	defer cancel()
	return exec.CommandContext(ctx, nft, "list", "tables").Run() == nil
}

// Loaded reports whether the table of this package is in the kernel now. A host where nft is missing, or does not answer, cannot
// say: that is an error, and not a no. The command runs in the C locale, so that "no such table" has the words it is told by.
func (NftFirewall) Loaded(ctx context.Context) (bool, error) {
	nft, err := findNft()
	if err != nil {
		return false, fmt.Errorf("portguard: nft (nftables) was not found: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, nftTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, nft, "list", "table", "inet", table)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if strings.Contains(string(out), "No such file or directory") {
		return false, nil
	}
	return false, fmt.Errorf("portguard: nft list table: %w: %s", err, strings.TrimSpace(string(out)))
}

// Apply replaces the rules with the ones that close ports.
func (NftFirewall) Apply(ctx context.Context, ports []Port) error {
	nft, err := findNft()
	if err != nil {
		return fmt.Errorf("portguard: nft (nftables) was not found: %w", err)
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

// Remove deletes the table; a table that is not there is what a host usually has, so it is not an error, and neither is a host
// where nft cannot be found, which has no rules of ours (Installed tells the two apart for a caller that has to be sure).
func (NftFirewall) Remove(ctx context.Context) error {
	nft, err := findNft()
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
