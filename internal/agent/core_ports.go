package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"slices"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// localPorts are the two loopback ports that only the agent talks to: the api inbound of its core and the metrics
// listener, which is probed at the address it listens on. The master renders every config from one template, so a server
// that runs another Xray holds both already.
type localPorts struct {
	api, metrics int
	metricsHost  string
}

// localize points the api inbound and a metrics listener on the loopback at the ports this core keeps. They are the ones
// of the config while those are free, and others once something holds them, and they stay until that or an inbound changes.
func (c *Core) localize(cfg *xray.Config) {
	api := slices.IndexFunc(cfg.InboundConfigs, func(in xray.InboundConfig) bool { return in.Tag == apiTag })
	if api < 0 {
		return
	}
	metricsHost, pushedMetrics := metricsListen(cfg.Metrics)
	taken := inboundPorts(cfg)

	// The probes are made outside the lock, which only guards the fields: build and start, the callers, hold applyMu.
	c.mu.Lock()
	ports := c.ports
	c.mu.Unlock()
	if ports.api == 0 || slices.Contains(taken, ports.api) {
		ports.api = chooseCorePort("api", cfg.InboundConfigs[api].Port, taken)
	}
	if pushedMetrics != 0 && (ports.metrics == 0 || ports.metricsHost != metricsHost || ports.metrics == ports.api || slices.Contains(taken, ports.metrics)) {
		ports.metrics = chooseCorePortAt(metricsHost, "metrics", pushedMetrics, append(slices.Clone(taken), ports.api))
		ports.metricsHost = metricsHost
	}
	c.mu.Lock()
	c.ports = ports
	c.mu.Unlock()

	cfg.InboundConfigs[api].Port = ports.api
	if pushedMetrics != 0 {
		cfg.Metrics = withMetricsPort(cfg.Metrics, ports.metrics)
	}
}

// forgetTakenPorts drops the ports that something holds now, so that localize chooses again. The core is not running
// when it starts, and its own ports are free then.
func (c *Core) forgetTakenPorts() {
	c.mu.Lock()
	ports := c.ports
	c.mu.Unlock()
	forgetIfHeld := func(port *int, host string) {
		if *port != 0 && !portFreeAt(host, *port) {
			logger.Warningf("agent: port %d of the core is held by something else, choosing another", *port)
			*port = 0
		}
	}
	forgetIfHeld(&ports.api, "127.0.0.1")
	forgetIfHeld(&ports.metrics, ports.metricsHost)
	c.mu.Lock()
	c.ports = ports
	c.mu.Unlock()
}

// chooseCorePort is chooseCorePortAt on the IPv4 loopback, where the api inbound listens.
func chooseCorePort(what string, preferred int, avoid []int) int {
	return chooseCorePortAt("127.0.0.1", what, preferred, avoid)
}

// chooseCorePortAt is the port asked for when it is free at host and no inbound of the config uses it, and a port that
// nothing holds there otherwise.
func chooseCorePortAt(host, what string, preferred int, avoid []int) int {
	if preferred > 0 && !slices.Contains(avoid, preferred) && portFreeAt(host, preferred) {
		return preferred
	}
	for range 20 {
		ln, err := listenLoopback(net.JoinHostPort(host, "0"))
		if err != nil {
			break
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if !slices.Contains(avoid, port) {
			logger.Warningf("agent: the %s port %d of the core is held by something else on this server, using %d", what, preferred, port)
			return port
		}
	}
	return preferred
}

func loopbackPortFree(port int) bool { return portFreeAt("127.0.0.1", port) }

// portFreeAt reports whether nothing holds the port at host, which is the address the listener will be bound at: a port that
// the IPv4 loopback has free may be held on the IPv6 one, and the other way round.
func portFreeAt(host string, port int) bool {
	ln, err := listenLoopback(net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

func listenLoopback(address string) (net.Listener, error) {
	var listen net.ListenConfig
	return listen.Listen(context.Background(), "tcp", address)
}

func inboundPorts(cfg *xray.Config) []int {
	ports := make([]int, 0, len(cfg.InboundConfigs))
	for _, in := range cfg.InboundConfigs {
		if in.Tag != apiTag {
			ports = append(ports, in.Port)
		}
	}
	return ports
}

// metricsListen is the loopback address and the port that the metrics section listens on, or "" and 0 when it listens nowhere
// or at another address, which is an exposure that the operator chose and is none of the agent's to move. The name localhost
// is the IPv4 loopback, where a name that resolves to both is bound first.
func metricsListen(raw json_util.RawMessage) (string, int) {
	var section struct {
		Listen string `json:"listen"`
	}
	if json.Unmarshal(raw, &section) != nil || section.Listen == "" {
		return "", 0
	}
	host, port, err := net.SplitHostPort(section.Listen)
	if err != nil {
		return "", 0
	}
	if addr, err := netip.ParseAddr(host); host != "localhost" && (err != nil || !addr.IsLoopback()) {
		return "", 0
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", 0
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	return host, n
}

// withMetricsPort is the metrics section with its listener on port, at the address it had.
func withMetricsPort(raw json_util.RawMessage, port int) json_util.RawMessage {
	var section map[string]json.RawMessage
	if json.Unmarshal(raw, &section) != nil {
		return raw
	}
	var listen string
	_ = json.Unmarshal(section["listen"], &listen)
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return raw
	}
	moved, err := json.Marshal(net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return raw
	}
	section["listen"] = moved
	out, err := json.Marshal(section)
	if err != nil {
		return raw
	}
	return out
}
