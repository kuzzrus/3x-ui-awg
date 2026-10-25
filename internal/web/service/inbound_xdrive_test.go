package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func xdriveInbound(port int, xdriveSettings string) *model.Inbound {
	return &model.Inbound{
		Enable: true, Listen: "127.0.0.1", Port: port, Protocol: model.VLESS,
		Settings:       `{"clients":[],"decryption":"` + goldenVlessDecryption + `"}`,
		StreamSettings: `{"network":"xdrive","security":"none","xdriveSettings":` + xdriveSettings + `}`,
	}
}

// xray-core 26.10.17 (#7182) refuses to start an XDRIVE inbound that is not VLESS with
// "decryption" set, and with it every other inbound of the config.
func TestSavePathsRefuseXDriveOutsideVlessEncryption(t *testing.T) {
	const local = `{"service":"local","remoteFolder":"/tmp/drive"}`
	vmess := xdriveInbound(18490, local)
	vmess.Protocol = model.VMESS
	vmess.Settings = `{"clients":[]}`
	plain := xdriveInbound(18491, local)
	plain.Settings = `{"clients":[],"decryption":"none"}`
	tests := []struct {
		name string
		save func(t *testing.T) error
		want string
	}{
		{"vmess inbound", func(t *testing.T) error {
			_, _, err := (&InboundService{}).AddInbound(vmess)
			return err
		}, "XDRIVE carries only VLESS, not vmess"},
		{"vless inbound without decryption", func(t *testing.T) error {
			_, _, err := (&InboundService{}).AddInbound(plain)
			return err
		}, "XDRIVE needs VLESS encryption"},
		{"xray template inbound without decryption", func(t *testing.T) error {
			return (&XraySettingService{}).SaveXraySetting(`{"inbounds":[{"tag":"drive","port":18492,"protocol":"vless",
				"settings":` + plain.Settings + `,"streamSettings":` + plain.StreamSettings + `}],
				"outbounds":[{"protocol":"freedom","tag":"direct"}]}`)
		}, "XDRIVE needs VLESS encryption"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupBulkDB(t)
			if err := tc.save(t); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("save err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// An XDRIVE inbound saved before xray-core 26.10.17 without VLESS encryption makes the core refuse
// the whole config at startup; the build leaves that inbound out so the others keep running.
func TestGetXrayConfig_LeavesOutAStoredXDriveInboundTheCoreRefuses(t *testing.T) {
	seedVlessInbound(t, "plain-drive", 43130, nil)
	if err := database.GetDB().Model(&model.Inbound{}).Where("tag = ?", "plain-drive").
		Update("stream_settings", `{"network":"xdrive","security":"none","xdriveSettings":{"service":"local","remoteFolder":"/tmp/shared"}}`).Error; err != nil {
		t.Fatalf("set stream: %v", err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	for _, ib := range cfg.InboundConfigs {
		if ib.Tag == "plain-drive" {
			t.Fatalf("XDRIVE inbound without decryption emitted: %s", ib.Settings)
		}
	}
}

// A template XDRIVE outbound from before 26.10.17 lacks the VLESS encryption and mux the core now
// requires; the build drops only that one instead of letting the core refuse the whole config.
func TestGetXrayConfig_DropsATemplateXDriveOutboundTheCoreRefuses(t *testing.T) {
	setupConflictDB(t)
	const stream = `{"network":"xdrive","security":"none","xdriveSettings":{"service":"local","remoteFolder":"/tmp/shared"}}`
	template := `{"outbounds":[{"protocol":"freedom","tag":"direct"},
		{"protocol":"vless","tag":"drive-plain","streamSettings":` + stream + `,
			"settings":{"address":"10.0.0.1","port":443,"id":"11111111-2222-4333-8444-555555555555","encryption":"none"}},
		{"protocol":"vless","tag":"drive-encrypted","streamSettings":` + stream + `,"mux":{"enabled":true,"concurrency":8},
			"settings":{"address":"10.0.0.1","port":443,"id":"11111111-2222-4333-8444-555555555555",
				"encryption":"mlkem768x25519plus.native.0rtt.mMFxPe7lz5xoq2qBk22cQYefu5fpc_2dGR8lMOKem0E"}}]}`
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	var outbounds []struct {
		Tag string `json:"tag"`
	}
	if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
		t.Fatalf("decode emitted outbounds: %v", err)
	}
	var tags []string
	for _, ob := range outbounds {
		tags = append(tags, ob.Tag)
	}
	if !slices.Equal(tags, []string{"direct", "drive-encrypted"}) {
		t.Fatalf("emitted outbound tags = %v, want [direct drive-encrypted]", tags)
	}
}

// Whatever reached the outbound that was dropped must be blocked, not left to fall through to the
// default outbound: a rule to it, a balancer of it alone, and the first place, which is the default.
func TestConfigBuildBlocksWhatReachedADroppedXDriveOutbound(t *testing.T) {
	setupSettingTestDB(t)
	node := seedAgentRow(t, "agent")
	t.Cleanup(func() { setAgentConfigProblem(node, "") })
	const stream = `{"network":"xdrive","security":"none","xdriveSettings":{"service":"local","remoteFolder":"/tmp/shared"}}`
	template := `{"outbounds":[
		{"protocol":"vless","tag":"drive-plain","streamSettings":` + stream + `,
			"settings":{"address":"10.0.0.1","port":443,"id":"11111111-2222-4333-8444-555555555555","encryption":"none"}},
		{"protocol":"freedom","tag":"direct"}],
		"routing":{"domainStrategy":"AsIs","rules":[
			{"type":"field","domain":["marker-rule"],"outboundTag":"drive-plain"},
			{"type":"field","domain":["marker-pool"],"balancerTag":"drive-pool"}],
			"balancers":[{"tag":"drive-pool","selector":["drive-plain"]}]}}`
	if err := (&SettingService{}).saveSetting("xrayTemplateConfig", template); err != nil {
		t.Fatalf("seed template: %v", err)
	}

	svc := &XrayService{}
	master, err := svc.GetXrayConfig()
	if err != nil {
		t.Fatalf("GetXrayConfig: %v", err)
	}
	agent, err := svc.GetAgentConfig(node)
	if err != nil {
		t.Fatalf("GetAgentConfig: %v", err)
	}
	for name, cfg := range map[string]*xray.Config{"the panel's core": master, "an agent's core": agent} {
		targets := ruleTargets(t, cfg)
		for marker, got := range targets {
			if got != "outbound "+missingOutboundTag {
				t.Errorf("%s: the rule %s sends to %q, want the blackhole", name, marker, got)
			}
		}
		if len(targets) != 2 {
			t.Errorf("%s: found the rules %v, want both of the template's", name, targets)
		}
		var outbounds []struct{ Protocol, Tag string }
		if err := json.Unmarshal(cfg.OutboundConfigs, &outbounds); err != nil {
			t.Fatal(err)
		}
		if len(outbounds) == 0 || outbounds[0].Protocol != "blackhole" || outbounds[0].Tag != missingOutboundTag {
			t.Errorf("%s: the default outbound is %+v, want the blackhole that takes the place of the dropped one", name, outbounds)
		}
	}
	if problem := agentConfigProblem(node); !strings.Contains(problem, `"drive-plain"`) {
		t.Errorf("the agent's problem is %q, want it to name the outbound that is gone", problem)
	}
}

// xray-core 26.10.17 renamed the service to "google-drive" and reads "Google Drive" only as a
// deprecated alias, so a save posting the old name stores the new one.
func TestAddInboundStoresTheGoogleDriveServiceUnderItsNewName(t *testing.T) {
	setupBulkDB(t)
	in, _, err := (&InboundService{}).AddInbound(xdriveInbound(18493,
		`{"service":"Google Drive","remoteFolder":"f","secrets":["id","secret","token"]}`))
	if err != nil {
		t.Fatalf("AddInbound: %v", err)
	}
	if !strings.Contains(in.StreamSettings, `"service": "google-drive"`) {
		t.Fatalf("stored streamSettings = %s, want service google-drive", in.StreamSettings)
	}
}

// A complete template whose basic login is the first two secrets, as the Yandex Disk one is.
const basicLoginXDriveTemplate = `{"auth":{"type":"basic","username":"{secret0}","password":"{secret1}"},` +
	`"put":{"url":"u"},"get":{"url":"u"},"delete":{"url":"u"},"list":{"url":"u","namesRegex":"(.+)"}}`

// xray-core opens XDRIVE storage when the listener starts; a storage it refuses there stops every inbound.
func TestAddInboundRefusesXDriveStorageTheCoreCannotOpen(t *testing.T) {
	tests := []struct {
		name     string
		settings string
		want     string
	}{
		{"local without folder", `{"service":"local","remoteFolder":""}`, `XDRIVE needs a "remoteFolder"`},
		{"google drive without folder", `{"service":"Google Drive","remoteFolder":" ","secrets":["id","secret","token"]}`, `XDRIVE needs a "remoteFolder"`},
		{"google drive with two secrets", `{"service":"Google Drive","remoteFolder":"f","secrets":["id","secret"]}`, "Google Drive needs 3 secrets"},
		{"google drive with an empty secret", `{"service":"Google Drive","remoteFolder":"f","secrets":["id","","token"]}`, "Google Drive needs 3 secrets"},
		{"template without template", `{"service":"template","remoteFolder":"f"}`, `XDRIVE service "template" needs a "template" object`},
		{"template that is empty", `{"service":"template","remoteFolder":"f","template":{}}`, "XDRIVE template needs put, get, list and delete operations"},
		{"template that is not an object", `{"service":"template","remoteFolder":"f","template":"put"}`, "XDRIVE template is not a valid storage template"},
		{"template without a delete op", `{"service":"template","template":{"put":{"url":"u"},"get":{"url":"u"},"list":{"url":"u","namesRegex":"(.+)"}}}`, "XDRIVE template needs put, get, list and delete operations"},
		{"template without namesRegex", `{"service":"template","template":{"put":{"url":"u"},"get":{"url":"u"},"list":{"url":"u"},"delete":{"url":"u"}}}`, "XDRIVE template list needs a namesRegex"},
		{"template namesRegex without a group", `{"service":"template","template":{"put":{"url":"u"},"get":{"url":"u"},"list":{"url":"u","namesRegex":"name"},"delete":{"url":"u"}}}`, "XDRIVE template namesRegex needs one capture group"},
		{"basic login with a blank username secret", `{"service":"template","remoteFolder":"f","secrets":["","pw"],"template":` + basicLoginXDriveTemplate + `}`, "basic-auth username"},
		{"basic login with a missing password secret", `{"service":"template","remoteFolder":"f","secrets":["login"],"template":` + basicLoginXDriveTemplate + `}`, "basic-auth password"},
		{"unknown service", `{"service":"Dropbox","remoteFolder":"f"}`, `unsupported XDRIVE service "Dropbox"`},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupBulkDB(t)
			_, _, err := (&InboundService{}).AddInbound(xdriveInbound(18460+i, tc.settings))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("AddInbound err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

func TestAddInboundAcceptsACompleteXDriveTemplate(t *testing.T) {
	setupBulkDB(t)
	tmpl := `{"service":"template","template":{"put":{"url":"u"},"get":{"url":"u"},"list":{"url":"u","namesRegex":"\"name\":\"([^\"]+)\""},"delete":{"url":"u"}}}`
	if _, _, err := (&InboundService{}).AddInbound(xdriveInbound(18480, tmpl)); err != nil {
		t.Fatalf("AddInbound of a template xray-core can open: %v", err)
	}
}

// Only a login that is nothing but a secret is judged: text around one, or another auth type, is the
// template author's business.
func TestAddInboundJudgesOnlyBasicLoginsThatAreNothingButASecret(t *testing.T) {
	setupBulkDB(t)
	const operations = `"put":{"url":"u"},"get":{"url":"u"},"delete":{"url":"u"},"list":{"url":"u","namesRegex":"(.+)"}`
	tests := []struct{ name, secrets, auth string }{
		{"text around the secrets", `[]`, `{"type":"basic","username":"bot-{secret0}","password":"pw-{secret1}"}`},
		{"another auth type", `[]`, `{"type":"oauth2","username":"{secret0}","password":"{secret1}"}`},
		{"a basic login with its secrets", `["login","pw"]`, `{"type":"basic","username":"{secret0}","password":"{secret1}"}`},
	}
	for i, tc := range tests {
		settings := `{"service":"template","remoteFolder":"f","secrets":` + tc.secrets +
			`,"template":{"auth":` + tc.auth + `,` + operations + `}}`
		if _, _, err := (&InboundService{}).AddInbound(xdriveInbound(18497+i, settings)); err != nil {
			t.Errorf("%s: AddInbound = %v, want it accepted", tc.name, err)
		}
	}
}

// The XDRIVE form offers Yandex Disk from a file the frontend imports; the save path must
// accept the stream the form makes from it, folder and credentials filled in.
func TestAddInboundAcceptsTheYandexDiskTemplateOfTheForm(t *testing.T) {
	setupBulkDB(t)
	template, err := os.ReadFile(filepath.Join("..", "..", "..", "frontend", "src", "lib", "xray", "xdrive-yandex.json"))
	if err != nil {
		t.Fatal(err)
	}
	settings := `{"service":"template","remoteFolder":"xdrive-test","secrets":["login","app-password"],"template":` + string(template) + `}`
	if _, _, err := (&InboundService{}).AddInbound(xdriveInbound(18496, settings)); err != nil {
		t.Fatalf("AddInbound with the Yandex Disk template: %v", err)
	}
}

func TestSavePathsRefuseXDriveWithoutStorage(t *testing.T) {
	const want = `XDRIVE needs a "remoteFolder"`
	const empty = `{"service":"local","remoteFolder":""}`
	tests := []struct {
		name string
		save func(t *testing.T) error
	}{
		{"update inbound", func(t *testing.T) error {
			in, _, err := (&InboundService{}).AddInbound(xdriveInbound(18470, `{"service":"local","remoteFolder":"/tmp/drive"}`))
			if err != nil {
				t.Fatalf("AddInbound of a complete XDRIVE inbound: %v", err)
			}
			in.StreamSettings = xdriveInbound(18470, empty).StreamSettings
			_, _, err = (&InboundService{}).UpdateInbound(in)
			return err
		}},
		{"xray template inbound", func(t *testing.T) error {
			return (&XraySettingService{}).SaveXraySetting(`{"inbounds":[{"tag":"drive","port":18471,"protocol":"vless",
				"settings":{"clients":[],"decryption":"none"},"streamSettings":` + xdriveInbound(18471, empty).StreamSettings + `}],
				"outbounds":[{"protocol":"freedom","tag":"direct"}]}`)
		}},
		{"xray template outbound", func(t *testing.T) error {
			return (&XraySettingService{}).SaveXraySetting(`{"outbounds":[{"protocol":"freedom","tag":"direct"},
				{"protocol":"vless","tag":"drive-out","settings":{"address":"drive.example","port":443,"id":"11111111-2222-4333-8444-555555555555","encryption":"none"},
				"streamSettings":` + xdriveInbound(0, empty).StreamSettings + `}]}`)
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setupBulkDB(t)
			if err := tc.save(t); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("save err = %v, want one containing %q", err, want)
			}
		})
	}
}
