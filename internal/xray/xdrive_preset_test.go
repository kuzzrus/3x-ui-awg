package xray

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The XDRIVE form fills its Yandex Disk preset from a file the frontend imports; the core builds
// an outbound that carries it without reading the operations, so the test names them itself.
func TestYandexDiskXDrivePresetIsBuiltByTheCore(t *testing.T) {
	template, err := os.ReadFile(filepath.Join("..", "..", "frontend", "src", "lib", "xray", "xdrive-yandex.json"))
	if err != nil {
		t.Fatal(err)
	}
	var operations map[string]json.RawMessage
	if err := json.Unmarshal(template, &operations); err != nil {
		t.Fatalf("the preset is not a JSON object: %v", err)
	}
	for _, name := range []string{"auth", "put", "get", "delete", "list"} {
		if _, ok := operations[name]; !ok {
			t.Fatalf("the preset has no %q", name)
		}
	}
	outbound := `{
		"protocol": "vless", "tag": "drive", "mux": {"enabled": true},
		"settings": {"address": "10.0.0.1", "port": 443, "id": "11111111-2222-4333-8444-555555555555",
			"encryption": "mlkem768x25519plus.native.0rtt.mMFxPe7lz5xoq2qBk22cQYefu5fpc_2dGR8lMOKem0E"},
		"streamSettings": {"network": "xdrive", "security": "none", "xdriveSettings": {
			"service": "template", "remoteFolder": "xdrive-test", "secrets": ["login", "app-password"],
			"segmentBytes": 262144, "flushIntervalMs": 100, "pollIntervalMs": 300, "maxPollIntervalMs": 1500,
			"sessionTtlSeconds": 120, "concurrency": 8, "template": ` + string(template) + `}}}`
	if err := ValidateOutboundConfig([]byte(outbound)); err != nil {
		t.Fatalf("xray-core refuses an outbound with the Yandex Disk preset: %v", err)
	}
}
