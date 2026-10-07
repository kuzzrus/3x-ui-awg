package agent

import (
	"encoding/json"
	"net"
	"testing"
)

const (
	markerFailTest  = "FAIL_TEST"
	markerFailStart = "FAIL_START"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// testConfig is a config the agent accepts. mark lands in the env section, which a
// running core can only take by restarting.
type testConfig struct {
	apiPort  int
	logLevel string
	mark     string
	inbounds []map[string]any
}

func (c testConfig) value() map[string]any {
	level := c.logLevel
	if level == "" {
		level = "warning"
	}
	inbounds := []any{map[string]any{
		"tag": "api", "listen": "127.0.0.1", "port": c.apiPort, "protocol": "tunnel",
		"settings": map[string]any{"rewriteAddress": "127.0.0.1"},
	}}
	for _, in := range c.inbounds {
		inbounds = append(inbounds, in)
	}
	cfg := map[string]any{
		"log":       map[string]any{"loglevel": level, "access": "/var/log/x-ui/access.log", "error": "none"},
		"api":       map[string]any{"tag": "api", "services": []string{"HandlerService", "StatsService", "RoutingService"}},
		"inbounds":  inbounds,
		"outbounds": []any{map[string]any{"protocol": "freedom", "tag": "direct"}},
		"routing": map[string]any{
			"rules": []any{map[string]any{"type": "field", "inboundTag": []string{"api"}, "outboundTag": "api"}},
		},
		"stats": map[string]any{},
		"policy": map[string]any{
			"levels": map[string]any{"0": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}},
			"system": map[string]any{"statsInboundUplink": true, "statsInboundDownlink": true},
		},
	}
	if c.mark != "" {
		cfg["env"] = map[string]any{"AGENT_TEST": c.mark}
	}
	return cfg
}

func (c testConfig) compact(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(c.value())
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
