package naiveproxy

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// Certificate modes of a NaiveProxy inbound: files the admin maintains, or a
// certificate the panel orders from Let's Encrypt and renews itself.
const (
	CertManual = "manual"
	CertAuto   = "auto"
)

// ErrCertPending is what starting an automatic-certificate inbound returns until its certificate exists.
var ErrCertPending = errors.New("the certificate has not been issued yet")

// CertState says what is happening to an inbound's certificate right now.
type CertState string

const (
	CertStateIdle      CertState = ""
	CertStateObtaining CertState = "obtaining"
	CertStateObtained  CertState = "obtained"
	CertStateFailed    CertState = "failed"
)

// CertStatus is what the inbound form shows. NotAfter is read from the file Caddy serves,
// State and Error from the ordering side, so a failed renewal still reports the old expiry.
type CertStatus struct {
	Mode     string     `json:"mode"`
	State    CertState  `json:"state"`
	NotAfter *time.Time `json:"notAfter,omitempty"`
	Error    string     `json:"error,omitempty"`
}

// CertRequest asks for an automatically managed certificate for Domain.
type CertRequest struct {
	Domain string
	Email  string // the ACME account contact, empty for none
}

// certStorageDir sits beside bin/naiveproxy, not in it: uninstalling the engine
// must not throw away the issued certificates and the ACME account.
func certStorageDir() string { return config.GetBinFolderPath() + "/naiveproxy-certs" }

type managedCert struct {
	email  string
	cancel context.CancelFunc
}

type certActivity struct {
	state CertState
	err   string
}

// certManager orders and renews the certificates of automatic-mode inbounds with CertMagic.
// Caddy cannot: it only ever sees loopback TCP handed over by the SNI relay.
type certManager struct {
	dir       string
	newIssuer func(cfg *certmagic.Config, email string) certmagic.Issuer
	// renewEvery is how often the cache scans for renewals, renewalRatio how much of a lifetime is
	// the renewal window; 0 keeps CertMagic's 10 minutes and a third.
	renewEvery   time.Duration
	renewalRatio float64

	mu        sync.Mutex // guards the fields below
	cache     *certmagic.Cache
	storage   *certmagic.FileStorage
	issuerKey string
	configs   map[string]*certmagic.Config // by ACME account email
	managed   map[string]managedCert       // by domain

	// Both are read from CertMagic's own goroutines, so neither is ever held across a call into it.
	byDomain sync.Map // domain -> *certmagic.Config
	actMu    sync.Mutex
	activity map[string]certActivity
}

func newCertManager(dir string) *certManager {
	return &certManager{
		dir: dir,
		newIssuer: func(cfg *certmagic.Config, email string) certmagic.Issuer {
			return certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
				CA:    certmagic.LetsEncryptProductionCA,
				Email: email,
				// HTTP-01 only: :443 belongs to Xray, so a TLS-ALPN challenge could never reach us.
				Agreed:                  true,
				DisableTLSALPNChallenge: true,
			})
		},
		activity: map[string]certActivity{},
	}
}

// setupLocked creates the cache on first use: a panel with no automatic inbound never pays for one.
func (m *certManager) setupLocked() {
	if m.cache != nil {
		return
	}
	m.storage = &certmagic.FileStorage{Path: m.dir}
	m.cache = certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert:   m.configForCert,
		RenewCheckInterval: m.renewEvery,
	})
	m.configs = map[string]*certmagic.Config{}
	m.managed = map[string]managedCert{}
	m.issuerKey = m.configLocked("").Issuers[0].IssuerKey()
}

func (m *certManager) configLocked(email string) *certmagic.Config {
	if cfg, ok := m.configs[email]; ok {
		return cfg
	}
	cfg := certmagic.New(m.cache, certmagic.Config{Storage: m.storage, OnEvent: m.onEvent, RenewalWindowRatio: m.renewalRatio})
	cfg.Issuers = []certmagic.Issuer{m.newIssuer(cfg, email)}
	m.configs[email] = cfg
	return cfg
}

// configForCert answers the cache's maintenance loop: which account renews this certificate.
func (m *certManager) configForCert(c certmagic.Certificate) (*certmagic.Config, error) {
	for _, name := range c.Names {
		if cfg, ok := m.byDomain.Load(name); ok {
			return cfg.(*certmagic.Config), nil
		}
	}
	return nil, errors.New("the certificate is no longer managed")
}

func (m *certManager) onEvent(_ context.Context, event string, data map[string]any) error {
	domain, _ := data["identifier"].(string)
	if _, ours := m.byDomain.Load(domain); !ours {
		return nil
	}
	switch event {
	case "cert_obtaining":
		m.setActivity(domain, certActivity{state: CertStateObtaining})
		logger.Infof("naiveproxy: ordering a certificate for %s", domain)
	case "cert_obtained":
		m.setActivity(domain, certActivity{state: CertStateObtained})
		logger.Infof("naiveproxy: certificate for %s is ready", domain)
	case "cert_failed":
		msg := "unknown error"
		if err, ok := data["error"].(error); ok {
			msg = err.Error()
		}
		// An order cancelled by retry or by dropping the domain is not a failure of the new one.
		if strings.Contains(msg, "context canceled") {
			return nil
		}
		m.setActivity(domain, certActivity{state: CertStateFailed, err: msg})
		logger.Warningf("naiveproxy: ordering the certificate for %s failed: %s", domain, msg)
	}
	return nil
}

func (m *certManager) setActivity(domain string, a certActivity) {
	m.actMu.Lock()
	m.activity[domain] = a
	m.actMu.Unlock()
}

func (m *certManager) activityOf(domain string) (certActivity, bool) {
	m.actMu.Lock()
	defer m.actMu.Unlock()
	a, ok := m.activity[domain]
	return a, ok
}

// request starts keeping domain's certificate valid. Asking again is a no-op,
// except under another account email, which moves the domain to that account.
func (m *certManager) request(req CertRequest) {
	domain := normalizeDomain(req.Domain)
	if err := ValidCertDomain(domain); err != nil {
		m.setActivity(domain, certActivity{state: CertStateFailed, err: err.Error()})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.setupLocked()
	if cur, ok := m.managed[domain]; ok {
		if cur.email == req.Email {
			return
		}
		m.stopLocked(domain)
	}
	m.startLocked(domain, req.Email)
}

func (m *certManager) startLocked(domain, email string) {
	cfg := m.configLocked(email)
	m.byDomain.Store(domain, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	m.managed[domain] = managedCert{email: email, cancel: cancel}
	state := CertStateObtaining
	if _, _, ok := m.lookupLocked(domain); ok {
		state = CertStateObtained
	}
	m.setActivity(domain, certActivity{state: state})
	if err := cfg.ManageAsync(ctx, []string{domain}); err != nil {
		m.setActivity(domain, certActivity{state: CertStateFailed, err: err.Error()})
	}
}

// stopLocked ends the retries and the renewals of domain; the certificate files stay.
func (m *certManager) stopLocked(domain string) {
	if cur, ok := m.managed[domain]; ok {
		cur.cancel()
		m.cache.RemoveManaged([]certmagic.SubjectIssuer{{Subject: domain}})
		delete(m.managed, domain)
	}
	m.byDomain.Delete(domain)
	m.actMu.Lock()
	delete(m.activity, domain)
	m.actMu.Unlock()
}

// retain stops looking after every domain outside keep.
func (m *certManager) retain(keep map[string]struct{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cache == nil {
		return
	}
	for domain := range m.managed {
		if _, ok := keep[domain]; !ok {
			m.stopLocked(domain)
		}
	}
	m.actMu.Lock()
	for domain := range m.activity {
		if _, ok := keep[domain]; !ok {
			delete(m.activity, domain)
		}
	}
	m.actMu.Unlock()
}

// retry drops the back-off of a failed order and tries again now.
func (m *certManager) retry(domain string) {
	if m == nil {
		return
	}
	domain = normalizeDomain(domain)
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.managed[domain]
	if !ok {
		return
	}
	m.stopLocked(domain)
	m.startLocked(domain, cur.email)
}

func (m *certManager) lookupLocked(domain string) (certFile, keyFile string, ok bool) {
	if m.storage == nil {
		return "", "", false
	}
	certFile = m.storage.Filename(certmagic.StorageKeys.SiteCert(m.issuerKey, domain))
	keyFile = m.storage.Filename(certmagic.StorageKeys.SitePrivateKey(m.issuerKey, domain))
	// A pair is only ready once both halves load together: the two files are written one after the other.
	if _, err := tls.LoadX509KeyPair(certFile, keyFile); err != nil {
		return "", "", false
	}
	return certFile, keyFile, true
}

// lookup returns domain's certificate and key files once a usable pair is on disk.
func (m *certManager) lookup(domain string) (certFile, keyFile string, ok bool) {
	if m == nil {
		return "", "", false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.lookupLocked(normalizeDomain(domain))
}

// status reports domain's certificate: the expiry of the file on disk, and what the ordering side is doing.
func (m *certManager) status(domain string) CertStatus {
	st := CertStatus{Mode: CertAuto}
	if m == nil {
		return st
	}
	domain = normalizeDomain(domain)
	if certFile, _, ok := m.lookup(domain); ok {
		st.State = CertStateObtained
		if leaf, err := certLeaf(certFile); err == nil {
			notAfter := leaf.NotAfter
			st.NotAfter = &notAfter
		}
	}
	if act, ok := m.activityOf(domain); ok {
		switch act.state {
		case CertStateObtaining:
			st.State = CertStateObtaining
		case CertStateFailed:
			st.State, st.Error = CertStateFailed, act.err
		}
	}
	return st
}

// stop ends every order and renewal, which is what the panel's shutdown wants. Safe to repeat.
func (m *certManager) stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for domain := range m.managed {
		m.stopLocked(domain)
	}
	if m.cache != nil {
		m.cache.Stop()
		m.cache = nil
	}
}

// SyncCerts orders a certificate for every request and stops looking after the
// automatic domains that are no longer asked for. Asking again is cheap.
func (m *Manager) SyncCerts(reqs []CertRequest) {
	m.mu.Lock()
	if m.certs == nil {
		if len(reqs) == 0 {
			m.mu.Unlock()
			return
		}
		m.certs = newCertManager(certStorageDir())
	}
	cm := m.certs
	m.mu.Unlock()

	keep := make(map[string]struct{}, len(reqs))
	for _, req := range reqs {
		cm.request(req)
		keep[normalizeDomain(req.Domain)] = struct{}{}
	}
	cm.retain(keep)
}

// AutoCertStatus reports the automatic certificate of domain, idle until it was requested.
func (m *Manager) AutoCertStatus(domain string) CertStatus {
	m.mu.Lock()
	cm := m.certs
	m.mu.Unlock()
	return cm.status(domain)
}

// RetryCert makes a failed automatic order start over now instead of at its next back-off.
func (m *Manager) RetryCert(domain string) {
	m.mu.Lock()
	cm := m.certs
	m.mu.Unlock()
	cm.retry(domain)
}
