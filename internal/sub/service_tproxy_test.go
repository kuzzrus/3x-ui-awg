package sub

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// tproxyTestSecretDD is dd-prefixed (34 chars) specifically so the link test
// below proves ClientLinkSecret's stripping is actually wired in, not just
// that a plain 32-hex value passes through unchanged.
const (
	tproxyTestSecretDD  = "dd0123456789abcdef0123456789abcdef"
	tproxyTestSecretRaw = "0123456789abcdef0123456789abcdef"
)

func TestGenTproxyLinkFields(t *testing.T) {
	initSubDB(t)
	settingService := service.SettingService{}
	if err := settingService.SetFrontProxyDomain("proxy.example.com"); err != nil {
		t.Fatalf("SetFrontProxyDomain: %v", err)
	}

	inbound := &model.Inbound{
		Protocol: model.Tproxy,
		Remark:   "tp-sub",
		Settings: `{"clients":[{"email":"user","enable":true,"tproxySecret":"` + tproxyTestSecretDD + `"}]}`,
	}

	s := &SubService{}
	link := s.genTproxyLink(inbound, "user")

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link does not parse: %v\n got: %s", err, link)
	}
	if u.Scheme != "tg" || u.Host != "webproxy" {
		t.Fatalf("link = %q, want a tg://webproxy deep link", link)
	}
	q := u.Query()
	if q.Get("server") != "proxy.example.com" {
		t.Fatalf("server = %q, want proxy.example.com", q.Get("server"))
	}
	if q.Get("secret") != tproxyTestSecretRaw {
		t.Fatalf("secret = %q, want the dd-prefix stripped to %q", q.Get("secret"), tproxyTestSecretRaw)
	}
	if q.Has("port") {
		t.Fatalf("link carries a port= parameter; the WEB proxy type fixes 443, want none: %q", link)
	}
	if u.Fragment != "" {
		t.Fatalf("link carries a #%s fragment; tg://webproxy links must have no remark fragment", u.Fragment)
	}
}

func TestGenTproxyLinkWrongProtocol(t *testing.T) {
	s := &SubService{}
	vless := &model.Inbound{Protocol: model.VLESS, Settings: `{"clients":[{"email":"user"}]}`}
	if got := s.genTproxyLink(vless, "user"); got != "" {
		t.Fatalf("wrong protocol should yield empty link, got %q", got)
	}
}

func TestGenTproxyLinkNoSecret(t *testing.T) {
	s := &SubService{}
	inbound := &model.Inbound{
		Protocol: model.Tproxy,
		Settings: `{"clients":[{"email":"user"}]}`,
	}
	if got := s.genTproxyLink(inbound, "user"); got != "" {
		t.Fatalf("client without a secret should yield empty link, got %q", got)
	}
}

func TestGenTproxyLinkNoFrontProxyDomain(t *testing.T) {
	initSubDB(t) // frontProxyDomain defaults to "" -- deliberately never set here

	inbound := &model.Inbound{
		Protocol: model.Tproxy,
		Settings: `{"clients":[{"email":"user","enable":true,"tproxySecret":"` + tproxyTestSecretRaw + `"}]}`,
	}
	s := &SubService{}
	if got := s.genTproxyLink(inbound, "user"); got != "" {
		t.Fatalf("unconfigured front-proxy domain should yield empty link, got %q", got)
	}
}

// Regression: a tproxy inbound must resolve for a subscription id the same
// way every other client-bearing protocol does -- mtproto was previously
// dropped from this exact allowlist (see TestGetInboundsBySubIdIncludesMtproto),
// and tproxy was never added to it at all despite genTproxyLink existing.
func TestGetInboundsBySubIdIncludesTproxy(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()
	settingService := service.SettingService{}
	if err := settingService.SetFrontProxyDomain("proxy.example.com"); err != nil {
		t.Fatalf("SetFrontProxyDomain: %v", err)
	}

	in := &model.Inbound{
		Protocol: model.Tproxy,
		Enable:   true,
		Tag:      "tp-sub",
		Settings: `{"clients":[{"email":"u@tp","enable":true,"subId":"subtp","tproxySecret":"` + tproxyTestSecretRaw + `"}]}`,
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rec := &model.ClientRecord{Email: "u@tp", SubID: "subtp", Enable: true, TproxySecret: tproxyTestSecretRaw}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: in.Id}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	s := &SubService{}
	inbounds, err := s.getInboundsBySubId("subtp")
	if err != nil {
		t.Fatalf("getInboundsBySubId: %v", err)
	}
	if len(inbounds) != 1 || inbounds[0].Id != in.Id {
		t.Fatalf("tproxy inbound not returned for subId: %+v", inbounds)
	}

	links, emails, _, _, err := s.GetSubs("subtp", "sub.example.com")
	if err != nil {
		t.Fatalf("GetSubs: %v", err)
	}
	if len(links) != 1 || len(emails) != 1 || emails[0] != "u@tp" {
		t.Fatalf("subscription did not emit the tproxy client: links=%v emails=%v", links, emails)
	}
	if !strings.HasPrefix(links[0], "tg://webproxy") || !strings.Contains(links[0], "secret="+tproxyTestSecretRaw) {
		t.Fatalf("subscription link is not a tg://webproxy carrying the client secret: %q", links[0])
	}
}
