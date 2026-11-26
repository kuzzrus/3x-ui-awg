package service

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// The config builder is the fork's renderInboundConfig: the devices of a WireGuard client come through
// it as peers of their own, beside the primary key, and the inbound's own list wins over the shared record.
func TestRenderInboundConfigCarriesEveryDeviceOfAWireGuardClient(t *testing.T) {
	setupConflictDB(t)
	client := model.Client{
		Email: "wg-devices-render", Enable: true, PublicKey: "primary-key", AllowedIPs: []string{"10.0.0.2/32"},
		WireguardDevices: []model.WireguardDevice{{Id: "phone", Enable: true, PublicKey: "phone-key", AllowedIPs: []string{"10.0.0.3/32"}}},
	}
	seedInboundConflict(t, "wg-render", "0.0.0.0", 50081, model.WireGuard, "", clientsSettings(t, []model.Client{client}))
	inbound := loadInboundByTag(t, "wg-render")
	if err := (&ClientService{}).SyncInbound(nil, inbound.Id, []model.Client{client}); err != nil {
		t.Fatal(err)
	}
	own := client
	own.WireguardDevices = []model.WireguardDevice{{Id: "tablet", Enable: true, PublicKey: "tablet-key", AllowedIPs: []string{"10.0.0.4/32"}}}
	inbound.Settings = clientsSettings(t, []model.Client{own})

	cfg, _, err := (&XrayService{}).renderInboundConfig(inbound)
	if err != nil {
		t.Fatalf("renderInboundConfig: %v", err)
	}
	var settings struct {
		Peers []struct {
			PublicKey string `json:"publicKey"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(cfg.Settings, &settings); err != nil {
		t.Fatalf("rendered settings are not JSON: %v", err)
	}
	var keys []string
	for _, p := range settings.Peers {
		keys = append(keys, p.PublicKey)
	}
	slices.Sort(keys)
	if want := []string{"primary-key", "tablet-key"}; !slices.Equal(keys, want) {
		t.Fatalf("rendered peers = %v, want %v", keys, want)
	}
}
