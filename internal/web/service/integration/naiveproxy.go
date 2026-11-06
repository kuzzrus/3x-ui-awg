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

// AutoStart brings an engine that an earlier version of the panel installed to the release this one pins. It runs
// beside the boot and returns at once; the reconcile job restarts the inbounds that run from the old file.
func (s *NaiveProxyService) AutoStart() { go s.refreshEngine() }

// engineRefreshAttempts and engineRefreshDelay retry the fetch: Xray's start only execs the core, and a panel-only
// restart never starts it, so the panel's own outbound may not carry the first attempt. Vars so tests can shorten them.
var (
	engineRefreshAttempts = 4
	engineRefreshDelay    = 10 * time.Minute
)

func (s *NaiveProxyService) refreshEngine() {
	if !naiveproxy.IsInstalled() {
		return // nothing to bring up to date, and the egress proxy setting is not read at the boot of every panel
	}
	for attempt := 1; ; attempt++ {
		replaced, err := s.fetchPinnedEngine()
		switch {
		case err == nil && replaced:
			logger.Info("naiveproxy: the installed engine was replaced by the pinned release")
			return
		case err == nil:
			return
		case attempt >= engineRefreshAttempts:
			logger.Warningf("naiveproxy: cannot bring the installed engine to the pinned release: %v", err)
			return
		}
		logger.Debugf("naiveproxy: cannot bring the installed engine to the pinned release yet: %v", err)
		time.Sleep(engineRefreshDelay)
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
