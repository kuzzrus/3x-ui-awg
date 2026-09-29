package sub

import (
	"net/url"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const naiveTestPassword = "Zx9kQ2mPvL7wRt4N"

func TestGenNaiveProxyLinkFields(t *testing.T) {
	inbound := &model.Inbound{
		Listen:   "127.0.0.1",
		Port:     18443,
		Protocol: model.NaiveProxy,
		Remark:   "np-sub",
		Settings: `{"domain":"naive.example.com","clients":[{"email":"user@naive","enable":true,"naiveProxyPassword":"` + naiveTestPassword + `"}]}`,
	}

	s := &SubService{}
	link := s.genNaiveProxyLink(inbound, "user@naive")

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link does not parse: %v\n got: %s", err, link)
	}
	if u.Scheme != "naive+https" {
		t.Fatalf("scheme = %q, want naive+https", u.Scheme)
	}
	if u.Hostname() != "naive.example.com" {
		t.Fatalf("host = %q, want the inbound's own domain", u.Hostname())
	}
	if u.Port() != "443" {
		t.Fatalf("port = %q, want the public 443, never the inbound's loopback port 18443", u.Port())
	}
	if u.User.Username() != "user@naive" {
		t.Fatalf("username = %q, want the client's email", u.User.Username())
	}
	if pw, _ := u.User.Password(); pw != naiveTestPassword {
		t.Fatalf("password = %q, want the client's naiveProxyPassword", pw)
	}
	if u.Fragment != "np-sub-user@naive" {
		t.Fatalf("fragment = %q, want the inbound remark plus email", u.Fragment)
	}
}

// A front door that is not on 443 must be dialled where it is reachable; a
// missing or out-of-range value falls back to 443 rather than emitting a dead link.
func TestGenNaiveProxyLinkPublicPort(t *testing.T) {
	cases := []struct {
		name string
		port string
		want string
	}{
		{"custom", `"publicPort":8443,`, "8443"},
		{"missing", ``, "443"},
		{"zero", `"publicPort":0,`, "443"},
		{"too large", `"publicPort":70000,`, "443"},
		{"fractional", `"publicPort":8443.5,`, "443"},
		{"not a number", `"publicPort":"8443",`, "443"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inbound := &model.Inbound{
				Protocol: model.NaiveProxy,
				Settings: `{"domain":"naive.example.com",` + tc.port + `"clients":[{"email":"user","enable":true,"naiveProxyPassword":"` + naiveTestPassword + `"}]}`,
			}
			s := &SubService{}
			u, err := url.Parse(s.genNaiveProxyLink(inbound, "user"))
			if err != nil {
				t.Fatalf("link does not parse: %v", err)
			}
			if u.Port() != tc.want {
				t.Fatalf("port = %q, want %q", u.Port(), tc.want)
			}
		})
	}
}

// Caddy's basic_auth compares the decoded pair, so reserved characters in a
// hand-set credential must survive the userinfo round trip.
func TestGenNaiveProxyLinkEscapesCredentials(t *testing.T) {
	const email = "a b+c@example.com"
	const password = "p:a@s/s+w#rd"
	inbound := &model.Inbound{
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"naive.example.com","clients":[{"email":"` + email + `","enable":true,"naiveProxyPassword":"` + password + `"}]}`,
	}

	s := &SubService{}
	link := s.genNaiveProxyLink(inbound, email)

	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("link does not parse: %v\n got: %s", err, link)
	}
	if u.Hostname() != "naive.example.com" {
		t.Fatalf("host = %q; a reserved character leaked out of the userinfo: %s", u.Hostname(), link)
	}
	if u.User.Username() != email {
		t.Fatalf("username = %q, want %q", u.User.Username(), email)
	}
	if pw, _ := u.User.Password(); pw != password {
		t.Fatalf("password = %q, want %q", pw, password)
	}
}

func TestGenNaiveProxyLinkWrongProtocol(t *testing.T) {
	s := &SubService{}
	vless := &model.Inbound{Protocol: model.VLESS, Settings: `{"domain":"naive.example.com","clients":[{"email":"user"}]}`}
	if got := s.genNaiveProxyLink(vless, "user"); got != "" {
		t.Fatalf("wrong protocol should yield empty link, got %q", got)
	}
}

func TestGenNaiveProxyLinkNoPassword(t *testing.T) {
	s := &SubService{}
	inbound := &model.Inbound{
		Protocol: model.NaiveProxy,
		Settings: `{"domain":"naive.example.com","clients":[{"email":"user","enable":true}]}`,
	}
	if got := s.genNaiveProxyLink(inbound, "user"); got != "" {
		t.Fatalf("client without a password should yield empty link, got %q", got)
	}
}

func TestGenNaiveProxyLinkNoDomain(t *testing.T) {
	s := &SubService{}
	inbound := &model.Inbound{
		Protocol: model.NaiveProxy,
		Settings: `{"clients":[{"email":"user","enable":true,"naiveProxyPassword":"` + naiveTestPassword + `"}]}`,
	}
	if got := s.genNaiveProxyLink(inbound, "user"); got != "" {
		t.Fatalf("inbound without a domain should yield empty link, got %q", got)
	}
}

// Regression: naiveproxy was missing from getInboundsBySubId's protocol
// allowlist, so a Naive client never appeared in any subscription at all.
func TestGetInboundsBySubIdIncludesNaiveProxy(t *testing.T) {
	initSubDB(t)
	db := database.GetDB()

	in := &model.Inbound{
		Protocol: model.NaiveProxy,
		Enable:   true,
		Tag:      "np-sub",
		Remark:   "np-sub",
		Settings: `{"domain":"naive.example.com","clients":[{"email":"u@np","enable":true,"subId":"subnp","naiveProxyPassword":"` + naiveTestPassword + `"}]}`,
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	rec := &model.ClientRecord{Email: "u@np", SubID: "subnp", Enable: true, NaiveProxyPassword: naiveTestPassword}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{ClientId: rec.Id, InboundId: in.Id}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	s := &SubService{}
	inbounds, err := s.getInboundsBySubId("subnp")
	if err != nil {
		t.Fatalf("getInboundsBySubId: %v", err)
	}
	if len(inbounds) != 1 || inbounds[0].Id != in.Id {
		t.Fatalf("naiveproxy inbound not returned for subId: %+v", inbounds)
	}

	links, emails, _, _, err := s.GetSubs("subnp", "sub.example.com")
	if err != nil {
		t.Fatalf("GetSubs: %v", err)
	}
	if len(links) != 1 || len(emails) != 1 || emails[0] != "u@np" {
		t.Fatalf("subscription did not emit the naiveproxy client: links=%v emails=%v", links, emails)
	}
	if !strings.HasPrefix(links[0], "naive+https://") || !strings.Contains(links[0], "@naive.example.com:443") {
		t.Fatalf("subscription link is not a naive+https:// link to the inbound's domain on 443: %q", links[0])
	}
}
