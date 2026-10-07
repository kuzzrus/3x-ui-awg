package sys

import "testing"

func TestIsVirtualInterface(t *testing.T) {
	tests := []struct {
		name    string
		virtual bool
	}{
		{"lo", true},
		{"lo0", true},
		{"docker0", true},
		{"br-1a2b3c", true},
		{"veth9f8e7d", true},
		{"virbr0", true},
		{"tun0", true},
		{"tap1", true},
		{"wg0", true},
		{"tailscale0", true},
		{"zt5u4abc", true},
		{"Loopback Pseudo-Interface 1", true},
		{"WG0", true},
		{"eth0", false},
		{"ens3", false},
		{"enp0s31f6", false},
		{"wlan0", false},
		{"bond0", false},
		{"Ethernet", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsVirtualInterface(tt.name); got != tt.virtual {
				t.Fatalf("IsVirtualInterface(%q) = %v, want %v", tt.name, got, tt.virtual)
			}
		})
	}
}
