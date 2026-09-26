package naiveproxy

import (
	"encoding/json"
	"fmt"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// InstanceFromInbound builds Instance from ib's own Settings JSON. ok is
// false only for a wrong protocol or unparsable Settings, never a client-less inbound.
func InstanceFromInbound(ib *model.Inbound) (Instance, bool) {
	if ib == nil || ib.Protocol != model.NaiveProxy {
		return Instance{}, false
	}
	// RouteThroughXray/XrayRoutePort deliberately not read yet: no
	// injectNaiveProxyEgress counterpart provisions that port on Xray's side.
	var parsed struct {
		Domain   string `json:"domain"`
		CertFile string `json:"certFile"`
		KeyFile  string `json:"keyFile"`
		Clients  []struct {
			Email              string `json:"email"`
			NaiveProxyPassword string `json:"naiveProxyPassword"`
			Enable             bool   `json:"enable"`
		} `json:"clients"`
	}
	if err := json.Unmarshal([]byte(ib.Settings), &parsed); err != nil {
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
		Id:         ib.Id,
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", ib.Port),
		Domain:     parsed.Domain,
		CertFile:   parsed.CertFile,
		KeyFile:    parsed.KeyFile,
		Clients:    clients,
	}, true
}
