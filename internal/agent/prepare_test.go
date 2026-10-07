package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareConfigAcceptsTheStockTemplate(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "web", "service", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfig(body, t.TempDir()); err != nil {
		t.Fatalf("the panel's own template is refused: %v", err)
	}
}

func TestPrepareConfigRefuses(t *testing.T) {
	edit := func(mutate func(cfg map[string]any)) []byte {
		cfg := testConfig{apiPort: 62789}.value()
		mutate(cfg)
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	apiInbound := func(cfg map[string]any) map[string]any {
		return cfg["inbounds"].([]any)[0].(map[string]any)
	}

	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"not json", []byte("{not json"), "config is not a valid Xray config"},
		{"not an object", []byte("[1]"), "config is not a valid Xray config"},
		{"no api inbound", edit(func(cfg map[string]any) { cfg["inbounds"] = []any{} }), `no inbound tagged "api"`},
		{"api inbound without a port", edit(func(cfg map[string]any) { apiInbound(cfg)["port"] = 0 }), `no inbound tagged "api" with a port`},
		{"api inbound on every interface", edit(func(cfg map[string]any) { apiInbound(cfg)["listen"] = "0.0.0.0" }), `must listen on 127.0.0.1, got "0.0.0.0"`},
		{"api inbound without a listen address", edit(func(cfg map[string]any) { delete(apiInbound(cfg), "listen") }), `must listen on 127.0.0.1, got ""`},
		{"api inbound on the ipv6 loopback", edit(func(cfg map[string]any) { apiInbound(cfg)["listen"] = "::1" }), `must listen on 127.0.0.1, got "::1"`},
		{"no stats section", edit(func(cfg map[string]any) { delete(cfg, "stats") }), `no "stats" section`},
		{"null stats section", edit(func(cfg map[string]any) { cfg["stats"] = nil }), `no "stats" section`},
		{"no policy", edit(func(cfg map[string]any) { delete(cfg, "policy") }), `statsUserUplink and statsUserDownlink for level 0`},
		{"policy without levels", edit(func(cfg map[string]any) { cfg["policy"] = map[string]any{} }), `statsUserUplink and statsUserDownlink for level 0`},
		{
			"client uplink counted but not downlink",
			edit(func(cfg map[string]any) {
				cfg["policy"] = map[string]any{"levels": map[string]any{"0": map[string]any{"statsUserUplink": true}}}
			}),
			`statsUserUplink and statsUserDownlink for level 0`,
		},
		{
			"counters switched on for another level only",
			edit(func(cfg map[string]any) {
				cfg["policy"] = map[string]any{"levels": map[string]any{"1": map[string]any{"statsUserUplink": true, "statsUserDownlink": true}}}
			}),
			`statsUserUplink and statsUserDownlink for level 0`,
		},
		{"no api section", edit(func(cfg map[string]any) { delete(cfg, "api") }), `no "api" section tagged "api"`},
		{"api section with another tag", edit(func(cfg map[string]any) { cfg["api"] = map[string]any{"tag": "x"} }), `no "api" section tagged "api"`},
		{
			"api section without the stats service",
			edit(func(cfg map[string]any) {
				cfg["api"] = map[string]any{"tag": "api", "services": []string{"HandlerService"}}
			}),
			`must enable StatsService`,
		},
		{
			"api section without the handler service",
			edit(func(cfg map[string]any) {
				cfg["api"] = map[string]any{"tag": "api", "services": []string{"StatsService"}}
			}),
			`must enable HandlerService`,
		},
		{"no routing rule for the api inbound", edit(func(cfg map[string]any) { cfg["routing"] = map[string]any{"rules": []any{}} }), `no rule sending the "api" inbound`},
		{
			"a rule for another inbound",
			edit(func(cfg map[string]any) {
				cfg["routing"] = map[string]any{"rules": []any{map[string]any{"inboundTag": []string{"other"}, "outboundTag": "api"}}}
			}),
			`no rule sending the "api" inbound`,
		},
		{"log section that is not an object", edit(func(cfg map[string]any) { cfg["log"] = 5 }), `the "log" section is not an object`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := prepareConfig(tt.body, t.TempDir())
			var refused *ConfigError
			if !errors.As(err, &refused) {
				t.Fatalf("error = %v, want a *ConfigError", err)
			}
			if !strings.Contains(refused.Reason, tt.want) {
				t.Fatalf("reason = %q, want it to contain %q", refused.Reason, tt.want)
			}
		})
	}
}

func TestPrepareConfigAcceptsATagGivenAsOneString(t *testing.T) {
	cfg := testConfig{apiPort: 62789}.value()
	cfg["routing"] = map[string]any{"rules": []any{map[string]any{"inboundTag": "api", "outboundTag": "api"}}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := prepareConfig(raw, t.TempDir()); err != nil {
		t.Fatalf("a single-string inboundTag is valid Xray config: %v", err)
	}
}

func TestPrepareConfigForcesLogPaths(t *testing.T) {
	logDir := filepath.Join(t.TempDir(), "logs")
	tests := []struct {
		name string
		log  any
		want map[string]string
	}{
		{
			"both paths are the master's",
			map[string]any{"loglevel": "info", "access": "/var/log/x-ui/access.log", "error": "/var/log/x-ui/error.log"},
			map[string]string{
				"loglevel": "info",
				"access":   filepath.Join(logDir, "access.log"),
				"error":    filepath.Join(logDir, "error.log"),
			},
		},
		{
			"stdout and disabled stay as they are",
			map[string]any{"access": "none", "error": ""},
			map[string]string{"access": "none", "error": ""},
		},
		{
			"a path that is already the agent's",
			map[string]any{"access": filepath.Join(logDir, "access.log")},
			map[string]string{"access": filepath.Join(logDir, "access.log")},
		},
		{
			"neither key present",
			map[string]any{"loglevel": "warning"},
			map[string]string{"loglevel": "warning"},
		},
		{
			"the operator's file names are kept",
			map[string]any{"access": "/srv/xray/clients.log", "error": "C:/old/panel/problems.log"},
			map[string]string{"access": filepath.Join(logDir, "clients.log"), "error": filepath.Join(logDir, "problems.log")},
		},
		{
			"off however it is spelled",
			map[string]any{"access": "None", "error": "  NONE "},
			map[string]string{"access": "None", "error": "  NONE "},
		},
		{
			"a path with padding",
			map[string]any{"access": " /var/log/x-ui/padded.log "},
			map[string]string{"access": filepath.Join(logDir, "padded.log")},
		},
		{
			"a path that names no file",
			map[string]any{"access": "/", "error": "/var/log/.."},
			map[string]string{"access": filepath.Join(logDir, "access.log"), "error": filepath.Join(logDir, "error.log")},
		},
		{
			"a key in another case reaches the same setting",
			map[string]any{"Access": "/var/log/x-ui/access.log", "ERROR": "none"},
			map[string]string{"access": filepath.Join(logDir, "access.log"), "error": "none"},
		},
		{
			"the exact key wins over its case variants",
			map[string]any{"access": "none", "Access": "/var/log/x-ui/access.log"},
			map[string]string{"access": "none"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig{apiPort: 62789}.value()
			cfg["log"] = tt.log
			raw, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := prepareConfig(raw, logDir)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(prepared.LogConfig, &got); err != nil {
				t.Fatalf("log section: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("log section = %v, want %v", got, tt.want)
			}
			for key, want := range tt.want {
				if got[key] != want {
					t.Fatalf("log.%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestPrepareConfigKeepsTheLogSectionBytesWhenNothingChanges(t *testing.T) {
	cfg := testConfig{apiPort: 62789}.value()
	cfg["log"] = map[string]any{"loglevel": "warning", "access": "none"}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareConfig(raw, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"access":"none","loglevel":"warning"}`; string(prepared.LogConfig) != want {
		t.Fatalf("log section = %s, want it untouched: %s", prepared.LogConfig, want)
	}
}
