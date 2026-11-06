package integration

import (
	"context"
	"errors"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// NaiveProxyService manages the NaiveProxy engine binary (internal/naiveproxy).
// Unlike Tor/Psiphon it has no on/off state: the engine runs per NaiveProxy inbound.
type NaiveProxyService struct {
	service.SettingService
	inboundService service.InboundService
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

// upgradeEngine brings the installed engine to the pinned release; a variable so that tests can answer for it.
var upgradeEngine = naiveproxy.Upgrade

// RefreshEngine brings an engine installed before the last panel update to the release this one pins. The boot calls it
// after Xray is started, because the fetch goes through the panel's own outbound, and it returns at once instead of waiting.
func (s *NaiveProxyService) RefreshEngine() { go s.refreshEngine() }

// engineRefreshDelays are the waits between fetch attempts, one per retry. The first failure is usually only the core
// not listening on the panel's own bridge yet, which is seconds; a host that is really offline needs the longer ones.
var engineRefreshDelays = []time.Duration{15 * time.Second, 90 * time.Second, 5 * time.Minute, 23 * time.Minute}

func (s *NaiveProxyService) refreshEngine() {
	if !naiveproxy.IsInstalled() {
		return // nothing to bring up to date, and the egress proxy setting is not read at the boot of every panel
	}
	for attempt := 0; ; attempt++ {
		replaced, err := s.fetchPinnedEngine()
		switch {
		case err != nil && attempt < len(engineRefreshDelays):
			logger.Debugf("naiveproxy: cannot bring the installed engine to the pinned release yet: %v", err)
			time.Sleep(engineRefreshDelays[attempt])
		case err != nil:
			logger.Warningf("naiveproxy: cannot bring the installed engine to the pinned release: %v", err)
			return
		case replaced:
			logger.Info("naiveproxy: the installed engine was replaced by the pinned release")
			return
		default:
			return
		}
	}
}

// fetchPinnedEngine is one attempt, through the outbound the panel has at that moment.
func (s *NaiveProxyService) fetchPinnedEngine() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), installTimeout)
	defer cancel()
	return upgradeEngine(ctx, s.NewProxiedHTTPClient(installTimeout))
}

// NaiveProxyCert is one inbound's certificate as its form shows it: for an automatic
// inbound what the panel has ordered, for a manual one the expiry of the file the admin set.
type NaiveProxyCert struct {
	InboundId int    `json:"inboundId"`
	Domain    string `json:"domain"`
	Enable    bool   `json:"enable"` // a disabled automatic inbound is never ordered, so its idle state is not "waiting"
	naiveproxy.CertStatus
}

// Certs reports the certificate of every local NaiveProxy inbound.
func (s *NaiveProxyService) Certs() ([]NaiveProxyCert, error) {
	targets, err := s.inboundService.NaiveProxyCertTargets()
	if err != nil {
		return nil, err
	}
	mgr := naiveproxy.GetManager()
	certs := make([]NaiveProxyCert, 0, len(targets))
	for _, t := range targets {
		status := naiveproxy.ManualCertStatus(t.CertFile)
		if t.Mode == naiveproxy.CertAuto {
			status = mgr.AutoCertStatus(t.Domain)
		}
		certs = append(certs, NaiveProxyCert{InboundId: t.InboundId, Domain: t.Domain, Enable: t.Enable, CertStatus: status})
	}
	return certs, nil
}

// RetryCert makes a failed automatic order of the inbound start over now, not at its next back-off.
func (s *NaiveProxyService) RetryCert(inboundId int) error {
	targets, err := s.inboundService.NaiveProxyCertTargets()
	if err != nil {
		return err
	}
	for _, t := range targets {
		if t.InboundId != inboundId {
			continue
		}
		if t.Mode != naiveproxy.CertAuto {
			return errors.New("the inbound uses its own certificate files")
		}
		naiveproxy.GetManager().RetryCert(t.Domain)
		return nil
	}
	return errors.New("no such NaiveProxy inbound")
}
