package agent

import "testing"

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
