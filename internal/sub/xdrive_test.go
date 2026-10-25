package sub

import (
	"encoding/json"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

// XDRIVE has no share-link format: a link would either drop the storage secrets both
// ends need or print them into every QR code, so only the JSON subscription carries it.
func TestGetLink_SkipsXdriveStreams(t *testing.T) {
	stream := `{"network":"xdrive","security":"none",
		"xdriveSettings":{"service":"Google Drive","remoteFolder":"xray-tunnel","secrets":["id","secret","token"]}}`
	inbound := flowTestInbound(stream, "none")

	if link := (&SubService{}).GetLink(inbound, "user"); link != "" {
		t.Fatalf("GetLink = %q, want no link for an XDRIVE stream", link)
	}
}

func TestXdriveMux(t *testing.T) {
	const accepted = `{"concurrency":4,"enabled":true,"xudpConcurrency":8,"xudpProxyUDP443":"allow"}`
	tests := []struct{ name, in, want string }{
		{"none configured", ``, `{"enabled":true}`},
		{"not an object", `[1]`, `{"enabled":true}`},
		{"null", `null`, `{"enabled":true}`},
		{"disabled", `{"enabled":false}`, `{"enabled":true}`},
		{"concurrency off", `{"enabled":true,"concurrency":-1}`, `{"enabled":true}`},
		{"xudp off", `{"enabled":true,"xudpConcurrency":-1}`, `{"enabled":true}`},
		{"udp 443 skipped", `{"enabled":true,"xudpProxyUDP443":"skip"}`, `{"enabled":true}`},
		{"accepted as it is", accepted, accepted},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := xdriveMux(tc.in); got != tc.want {
				t.Fatalf("xdriveMux(%q) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

// xray-core 26.10.17 dials XDRIVE only through Mux.Cool and refuses the whole client config when the
// outbound lacks it (#7182); the JSON subscription is the only way a client gets one, so it must carry it.
func TestSubJsonServiceGivesAnXdriveOutboundTheMuxTheCoreNeeds(t *testing.T) {
	const encryption = "mlkem768x25519plus.native.0rtt.mMFxPe7lz5xoq2qBk22cQYefu5fpc_2dGR8lMOKem0E"
	inbound := &model.Inbound{
		Listen: "203.0.113.1", Port: 8443, Protocol: model.VLESS, Remark: "drive",
		Settings:       `{"clients":[{"id":"11111111-2222-4333-8444-555555555555","email":"user"}],"decryption":"none","encryption":"` + encryption + `"}`,
		StreamSettings: `{"network":"xdrive","security":"none","xdriveSettings":{"service":"local","remoteFolder":"/tmp/shared"}}`,
	}
	client := model.Client{ID: "11111111-2222-4333-8444-555555555555", Email: "user"}

	for name, global := range map[string]string{
		"no subJsonMux":     "",
		"mux off":           `{"enabled":false}`,
		"concurrency off":   `{"enabled":true,"concurrency":-1}`,
		"udp 443 skipped":   `{"enabled":true,"xudpProxyUDP443":"skip"}`,
		"a mux to keep":     `{"enabled":true,"concurrency":4,"xudpConcurrency":8,"xudpProxyUDP443":"allow"}`,
		"xudp switched off": `{"enabled":true,"xudpConcurrency":-1}`,
	} {
		t.Run(name, func(t *testing.T) {
			configs := NewSubJsonService(global, "", "", "", nil).getConfig(&SubService{address: "sub.example.com"}, inbound, client, "sub.example.com")
			if len(configs) != 1 {
				t.Fatalf("got %d configs, want one", len(configs))
			}
			var config struct {
				Outbounds []json.RawMessage `json:"outbounds"`
			}
			if err := json.Unmarshal(configs[0], &config); err != nil || len(config.Outbounds) == 0 {
				t.Fatalf("decode config: %v", err)
			}
			if err := xray.ValidateOutboundConfig(config.Outbounds[0]); err != nil {
				t.Fatalf("xray-core refuses the XDRIVE outbound of the subscription: %v\n%s", err, config.Outbounds[0])
			}
		})
	}
}
