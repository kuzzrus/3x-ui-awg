package agent

import (
	"context"
	"encoding/json"
	"net"
	"slices"
	"strconv"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// localPorts are the two loopback ports that only the agent talks to: the api inbound of its core and the metrics
// listener. The master renders every config from one template, so a server that runs another Xray holds both already.
type localPorts struct{ api, metrics int }

// localize points the api inbound and the metrics listener of cfg at the ports this core keeps. They are the ones of
// the config while those are free, and others once something holds them, which stay the same until that changes.
func (c *Core) localize(cfg *xray.Config) {
	api := slices.IndexFunc(cfg.InboundConfigs, func(in xray.InboundConfig) bool { return in.Tag == apiTag })
	if api < 0 {
		return
	}
	pushedMetrics := metricsPort(cfg.Metrics)
	taken := inboundPorts(cfg)

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ports.api == 0 {
		c.ports.api = chooseCorePort("api", cfg.InboundConfigs[api].Port, taken)
	}
	if c.ports.metrics == 0 && pushedMetrics != 0 {
		c.ports.metrics = chooseCorePort("metrics", pushedMetrics, append(taken, c.ports.api))
	}
	cfg.InboundConfigs[api].Port = c.ports.api
	if c.ports.metrics != 0 {
		cfg.Metrics = withMetricsPort(cfg.Metrics, c.ports.metrics)
	}
}

// forgetTakenPorts drops the ports that something holds now, so that localize chooses again. The core is not running
// when it starts, and its own ports are free then.
func (c *Core) forgetTakenPorts() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, port := range []*int{&c.ports.api, &c.ports.metrics} {
		if *port != 0 && !loopbackPortFree(*port) {
			logger.Warningf("agent: port %d of the core is held by something else, choosing another", *port)
			*port = 0
		}
	}
}

// chooseCorePort is the port asked for when it is free and no inbound of the config uses it, and a port that nothing
// holds otherwise.
func chooseCorePort(what string, preferred int, avoid []int) int {
	if preferred > 0 && !slices.Contains(avoid, preferred) && loopbackPortFree(preferred) {
		return preferred
	}
	for range 20 {
		ln, err := listenLoopback("127.0.0.1:0")
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

func loopbackPortFree(port int) bool {
	ln, err := listenLoopback(net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
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

// metricsPort is the port the metrics section listens on, or 0 when it listens nowhere.
func metricsPort(raw json_util.RawMessage) int {
	var section struct {
		Listen string `json:"listen"`
	}
	if json.Unmarshal(raw, &section) != nil || section.Listen == "" {
		return 0
	}
	_, port, err := net.SplitHostPort(section.Listen)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
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
