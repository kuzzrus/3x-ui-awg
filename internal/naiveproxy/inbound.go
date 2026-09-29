package naiveproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// FreeLocalPort asks the OS for a loopback TCP port and releases it, for the
// Xray egress bridge (internal/web/service's normalizeNaiveProxyXrayPort).
func FreeLocalPort() (int, error) {
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// InstanceFromInbound builds Instance from ib's own Settings JSON. ok is
// false only for a wrong protocol or unparsable Settings, never a client-less inbound.
func InstanceFromInbound(ib *model.Inbound) (Instance, bool) {
	if ib == nil || ib.Protocol != model.NaiveProxy {
		return Instance{}, false
	}
	var parsed struct {
		Domain           string `json:"domain"`
		CertFile         string `json:"certFile"`
		KeyFile          string `json:"keyFile"`
		RouteThroughXray bool   `json:"routeThroughXray"`
		RouteXrayPort    int    `json:"routeXrayPort"`
		Clients          []struct {
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
		Id:               ib.Id,
		ListenAddr:       fmt.Sprintf("127.0.0.1:%d", ib.Port),
		Domain:           parsed.Domain,
		CertFile:         parsed.CertFile,
		KeyFile:          parsed.KeyFile,
		RouteThroughXray: parsed.RouteThroughXray,
		XrayRoutePort:    parsed.RouteXrayPort,
		Clients:          clients,
	}, true
}
