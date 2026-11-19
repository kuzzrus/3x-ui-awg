package amneziawg

import (
	"slices"
	"testing"
)

func TestTunnelAddresses(t *testing.T) {
	cases := []struct {
		name string
		ips  []string
		want []string
	}{
		{"one address", []string{"10.8.1.2/32"}, []string{"10.8.1.2/32"}},
		{"both families", []string{"10.8.1.2/32", "fd86::2/128"}, []string{"10.8.1.2/32", "fd86::2/128"}},
		{"the order of the entries is kept", []string{"fd86::2/128", "10.8.1.2/32"}, []string{"fd86::2/128", "10.8.1.2/32"}},
		{"bare addresses are single hosts", []string{"10.8.1.2", "fd86::2"}, []string{"10.8.1.2", "fd86::2"}},
		{"a network after the address", []string{"10.8.1.5/32", "192.168.50.0/24"}, []string{"10.8.1.5/32"}},
		{"a network before the address", []string{"192.168.50.0/24", "10.8.1.5/32"}, []string{"10.8.1.5/32"}},
		{"an IPv6 network and no IPv6 address", []string{"10.8.1.5/32", "fd00:50::/64"}, []string{"10.8.1.5/32"}},
		{
			"a network in each family",
			[]string{"10.8.1.5/32", "192.168.50.0/24", "fd86::5/128", "fd00:50::/64"},
			[]string{"10.8.1.5/32", "fd86::5/128"},
		},
		{"a second single host is a network", []string{"10.8.1.2/32", "10.8.1.3/32"}, []string{"10.8.1.2/32"}},
		{"default routes after the address", []string{"10.8.1.2/32", "0.0.0.0/0", "::/0"}, []string{"10.8.1.2/32"}},
		{"an IPv4 address with a mask, as a legacy client has it", []string{"10.8.1.2/24"}, []string{"10.8.1.2/24"}},
		{"an IPv4 list with nothing single-host keeps its first", []string{"192.168.50.0/24", "10.9.0.0/16"}, []string{"192.168.50.0/24"}},
		{"an IPv6 list with nothing single-host has no address", []string{"10.8.1.2/32", "fd86::/64"}, []string{"10.8.1.2/32"}},
		{"entries that are not addresses are skipped", []string{"not-an-ip", "10.8.1.2/32"}, []string{"10.8.1.2/32"}},
		{"an address out of range is skipped", []string{"10.8.1.256/32", "10.8.1.2/32"}, []string{"10.8.1.2/32"}},
		{"a mask out of range is skipped", []string{"10.8.1.9/33"}, []string{}},
		{"an IPv6 mask out of range is skipped", []string{"fd86::9/129"}, []string{}},
		{"a v4-mapped v6 address is neither", []string{"::ffff:10.8.1.2/128"}, []string{}},
		{"spaces are trimmed", []string{" 10.8.1.2/32 "}, []string{"10.8.1.2/32"}},
		{"nothing", nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TunnelAddresses(c.ips); !slices.Equal(got, c.want) {
				t.Errorf("TunnelAddresses(%v) = %v, want %v", c.ips, got, c.want)
			}
		})
	}
}
