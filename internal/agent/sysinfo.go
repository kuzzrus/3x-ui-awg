package agent

import (
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"
)

// sysSample is the host's load as the master shows it on the node.
type sysSample struct {
	cpu     float64
	mem     float64
	uptime  uint64
	netUp   uint64
	netDown uint64
}

// sysSampler turns the host's counters into rates, so it needs to be asked regularly.
type sysSampler struct {
	mu   sync.Mutex
	at   time.Time
	sent uint64
	recv uint64
}

func (s *sysSampler) sample() sysSample {
	var out sysSample
	if pct, err := cpu.Percent(0, false); err == nil && len(pct) == 1 {
		out.cpu = pct[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		out.mem = vm.UsedPercent
	}
	if up, err := host.Uptime(); err == nil {
		out.uptime = up
	}

	counters, err := psnet.IOCounters(true)
	if err != nil {
		return out
	}
	var sent, recv uint64
	for _, c := range counters {
		if !virtualInterface(strings.ToLower(c.Name)) {
			sent += c.BytesSent
			recv += c.BytesRecv
		}
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if secs := now.Sub(s.at).Seconds(); !s.at.IsZero() && secs > 0 && sent >= s.sent && recv >= s.recv {
		out.netUp = uint64(float64(sent-s.sent) / secs)
		out.netDown = uint64(float64(recv-s.recv) / secs)
	}
	s.at, s.sent, s.recv = now, sent, recv
	return out
}

// virtualInterface skips loopback and the tunnels, bridges and container links, which
// would count the same bytes again.
func virtualInterface(name string) bool {
	if name == "lo" || name == "lo0" {
		return true
	}
	for _, prefix := range []string{"loopback", "docker", "br-", "veth", "virbr", "tun", "tap", "wg", "tailscale", "zt"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}
