package naiveproxy

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// inboundSettings is what the panel reads out of a NaiveProxy inbound's Settings JSON.
type inboundSettings struct {
	Domain           string `json:"domain"`
	CertMode         string `json:"certMode"`
	CertFile         string `json:"certFile"`
	KeyFile          string `json:"keyFile"`
	ACMEEmail        string `json:"acmeEmail"`
	RouteThroughXray bool   `json:"routeThroughXray"`
	RouteXrayPort    int    `json:"routeXrayPort"`
	Clients          []struct {
		Email              string `json:"email"`
		NaiveProxyPassword string `json:"naiveProxyPassword"`
		Enable             bool   `json:"enable"`
	} `json:"clients"`
}

// parseSettings is false only for a wrong protocol or unparsable Settings.
func parseSettings(ib *model.Inbound) (inboundSettings, bool) {
	var parsed inboundSettings
	if ib == nil || ib.Protocol != model.NaiveProxy {
		return parsed, false
	}
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
		return parsed, false
	}
	return parsed, true
}

// certMode is the explicit "auto", or else the files-the-admin-maintains behaviour inbounds had before the mode existed.
func (s inboundSettings) certMode() string {
	if s.CertMode == CertAuto {
		return CertAuto
	}
	return CertManual
}

// InstanceFromInbound builds Instance from ib's own Settings JSON. ok is
// false only for a wrong protocol or unparsable Settings, never a client-less inbound.
func InstanceFromInbound(ib *model.Inbound) (Instance, bool) {
	parsed, ok := parseSettings(ib)
	if !ok {
		return Instance{}, false
	}

	clients := make([]Client, 0, len(parsed.Clients))
	for _, c := range parsed.Clients {
		if !c.Enable || c.Email == "" || c.NaiveProxyPassword == "" {
			continue
		}
		// Email doubles as the Caddy basic_auth username -- no separate
		// username field, matching model.Client's own NaiveProxyPassword doc.
		clients = append(clients, Client{Email: c.Email, Username: c.Email, Password: c.NaiveProxyPassword})
	}

	return Instance{
		Id:               ib.Id,
		Tag:              ib.Tag,
		ListenAddr:       fmt.Sprintf("127.0.0.1:%d", ib.Port),
		Domain:           parsed.Domain,
		CertMode:         parsed.certMode(),
		CertFile:         parsed.CertFile,
		KeyFile:          parsed.KeyFile,
		RouteThroughXray: parsed.RouteThroughXray,
		XrayRoutePort:    parsed.RouteXrayPort,
		Clients:          clients,
	}, true
}

// CertSettings is the certificate half of a NaiveProxy inbound's settings.
type CertSettings struct {
	Domain   string
	Mode     string // CertAuto or CertManual
	CertFile string
	Email    string // the inbound's own ACME contact; empty means the reverse proxy's
}

// CertSettingsFromInbound reads the certificate settings of ib; ok is false as for InstanceFromInbound.
func CertSettingsFromInbound(ib *model.Inbound) (CertSettings, bool) {
	parsed, ok := parseSettings(ib)
	if !ok {
		return CertSettings{}, false
	}
	return CertSettings{Domain: parsed.Domain, Mode: parsed.certMode(), CertFile: parsed.CertFile, Email: parsed.ACMEEmail}, true
}
