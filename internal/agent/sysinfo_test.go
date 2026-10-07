package agent

import "testing"

func TestVirtualInterface(t *testing.T) {
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
		{"eth0", false},
		{"ens3", false},
		{"enp0s31f6", false},
		{"wlan0", false},
		{"bond0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := virtualInterface(tt.name); got != tt.virtual {
				t.Fatalf("virtualInterface(%q) = %v, want %v", tt.name, got, tt.virtual)
			}
		})
	}
}

func TestSysSamplerReportsFiguresInRange(t *testing.T) {
	var s sysSampler
	first := s.sample()
	if first.netUp != 0 || first.netDown != 0 {
		t.Fatalf("the first sample has no earlier one to measure a rate against, got up %d down %d", first.netUp, first.netDown)
	}
	second := s.sample()
	for _, sample := range []sysSample{first, second} {
		if sample.mem <= 0 || sample.mem > 100 || sample.cpu < 0 || sample.cpu > 100 || sample.uptime == 0 {
			t.Fatalf("sample = %+v is out of range", sample)
		}
	}
}
