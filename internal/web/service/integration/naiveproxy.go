package integration

import (
	"context"

	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// NaiveProxyService manages the NaiveProxy engine binary (internal/naiveproxy).
// Unlike Tor/Psiphon it has no on/off state: the engine runs per NaiveProxy inbound.
type NaiveProxyService struct {
	service.SettingService
}

// NaiveProxyStatus tells the inbound form whether the engine is present and,
// when it isn't, whether it can be installed on this host at all.
type NaiveProxyStatus struct {
	Installed bool   `json:"installed"`
	Supported bool   `json:"supported"`
	Platform  string `json:"platform"`
}

func (s *NaiveProxyService) Status() NaiveProxyStatus {
	return NaiveProxyStatus{
		Installed: naiveproxy.IsInstalled(),
		Supported: naiveproxy.Supported(),
		Platform:  naiveproxy.Platform(),
	}
}

// Install downloads the pinned engine via the panel's own proxied HTTP client.
// installTimeout is shared with AdGuardService (adguard.go).
func (s *NaiveProxyService) Install() error {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	return naiveproxy.Install(ctx, s.NewProxiedHTTPClient(installTimeout))
}
