package amneziawg

import (
	"net/netip"
	"slices"
	"strings"
)

// TunnelAddresses are the entries of a client's AllowedIPs that are its own end of the tunnel: the first single-host one (/32,
// /128) of each family. Every other entry is a network behind it; an IPv4 list with no single-host entry keeps its first.
func TunnelAddresses(allowedIPs []string) []string {
	v4, v6, v4First := -1, -1, -1
	for i, raw := range allowedIPs {
		prefix, ok := parseAllowedIP(raw)
		if !ok {
			continue
		}
		addr := prefix.Addr()
		host := prefix.Bits() == addr.BitLen()
		switch {
		case addr.Is4():
			if v4First < 0 {
				v4First = i
			}
			if host && v4 < 0 {
				v4 = i
			}
		case !addr.Is4In6():
			if host && v6 < 0 {
				v6 = i
			}
		}
	}
	if v4 < 0 {
		v4 = v4First
	}
	picked := make([]int, 0, 2)
	for _, i := range []int{v4, v6} {
		if i >= 0 {
			picked = append(picked, i)
		}
	}
	slices.Sort(picked)
	out := make([]string, 0, len(picked))
	for _, i := range picked {
		out = append(out, strings.TrimSpace(allowedIPs[i]))
	}
	return out
}

// parseAllowedIP reads an entry as a prefix, a bare address as a single host.
func parseAllowedIP(raw string) (netip.Prefix, bool) {
	raw = strings.TrimSpace(raw)
	if prefix, err := netip.ParsePrefix(raw); err == nil {
		return prefix, true
	}
	if addr, err := netip.ParseAddr(raw); err == nil {
		return netip.PrefixFrom(addr, addr.BitLen()), true
	}
	return netip.Prefix{}, false
}
