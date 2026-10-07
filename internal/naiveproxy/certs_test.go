package naiveproxy

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

// fakeIssuer stands in for Let's Encrypt: it signs whatever request it gets with a throwaway CA.
type fakeIssuer struct {
	life  time.Duration
	ca    *x509.Certificate
	key   *ecdsa.PrivateKey
	caPEM []byte

	mu    sync.Mutex
	calls int
	err   error
	gate  chan struct{} // when set, Issue waits for it to close
}

func newFakeIssuer(t *testing.T, life time.Duration) *fakeIssuer {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeIssuer{life: life, ca: ca, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func (f *fakeIssuer) IssuerKey() string { return "fake-ca" }

func (f *fakeIssuer) Issue(ctx context.Context, csr *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	f.mu.Lock()
	f.calls++
	err, gate := f.err, f.gate
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	now := time.Now()
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()),
		Subject:      pkix.Name{CommonName: csr.DNSNames[0]},
		DNSNames:     csr.DNSNames,
		NotBefore:    now,
		NotAfter:     now.Add(f.life),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, f.ca, csr.PublicKey, f.key)
	if err != nil {
		return nil, err
	}
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), f.caPEM...)
	return &certmagic.IssuedCertificate{Certificate: chain}, nil
}

func (f *fakeIssuer) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeIssuer) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

// newTestCertManager keeps every certificate in a temp dir and orders it from issuer.
func newTestCertManager(t *testing.T, issuer certmagic.Issuer, renewEvery time.Duration) *certManager {
	t.Helper()
	dir := t.TempDir()
	m := newCertManager(dir)
	m.newIssuer = func(*certmagic.Config, string) certmagic.Issuer { return issuer }
	m.renewEvery = renewEvery
	t.Cleanup(func() {
		m.stop()
		// CertMagic's goroutines end a moment after the cancel; let them drop their storage locks
		// before the temp dir goes, or its removal races their last writes.
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if locks, _ := os.ReadDir(filepath.Join(dir, "locks")); len(locks) == 0 {
				return
			}
		}
	})
	return m
}

// newRenewingCertManager renews a certificate soon after it is issued: the fake CA's last for 8 s
// and the final 80% of that is the window, which leaves a hiccup of a few seconds harmless.
func newRenewingCertManager(t *testing.T, issuer certmagic.Issuer) *certManager {
	t.Helper()
	m := newTestCertManager(t, issuer, 20*time.Millisecond)
	m.renewalRatio = 0.8
	return m
}

func waitForStatus(t *testing.T, m *certManager, domain string, want CertState) CertStatus {
	t.Helper()
	var st CertStatus
	waitUntil(t, "the certificate to be "+string(want), func() bool {
		st = m.status(domain)
		return st.State == want
	})
	return st
}

func TestCertManagerOrdersACertificate(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	m := newTestCertManager(t, issuer, 0)

	m.request(CertRequest{Domain: "Naive.Example.com", Email: "admin@example.com"})
	st := waitForStatus(t, m, "naive.example.com", CertStateObtained)

	if st.Mode != CertAuto || st.NotAfter == nil {
		t.Fatalf("status = %+v, want mode auto with an expiry", st)
	}
	if left := time.Until(*st.NotAfter); left < 50*time.Minute || left > time.Hour {
		t.Errorf("expires in %v, want about an hour", left)
	}
	certFile, keyFile, ok := m.lookup("naive.example.com")
	if !ok {
		t.Fatal("lookup found no certificate after the status said obtained")
	}
	leaf, err := certLeaf(certFile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(leaf.DNSNames, []string{"naive.example.com"}) {
		t.Errorf("certificate names = %v, want the lowercased domain only", leaf.DNSNames)
	}
	if info, err := os.Stat(keyFile); err != nil || info.Size() == 0 {
		t.Errorf("key file %s: %v", keyFile, err)
	}
}

func TestCertManagerReportsAFailedOrder(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	issuer.setErr(errors.New("the CA refused the challenge"))
	m := newTestCertManager(t, issuer, 0)

	m.request(CertRequest{Domain: "naive.example.com"})
	st := waitForStatus(t, m, "naive.example.com", CertStateFailed)

	if !strings.Contains(st.Error, "the CA refused the challenge") {
		t.Errorf("error = %q, want the CA's reason", st.Error)
	}
	if st.Hint != "" {
		t.Errorf("hint = %q for a failure that says nothing about port 80", st.Hint)
	}
	if st.NotAfter != nil {
		t.Errorf("expiry = %v for a certificate that was never issued", st.NotAfter)
	}
	if _, _, ok := m.lookup("naive.example.com"); ok {
		t.Error("lookup reports a certificate that was never issued")
	}
}

// What Let's Encrypt answers when another program holds port 80 (captured from a real order): the
// token is fetched from that program, not from the panel. Only such failures get the hint.
func TestCertHint(t *testing.T) {
	cases := []struct {
		name, msg, want string
	}{
		{"another program answers on port 80", `[app.example.com] solving challenge: authorization failed: HTTP 403 urn:ietf:params:acme:error:unauthorized - 203.0.113.7: Invalid response from http://app.example.com/.well-known/acme-challenge/tok: 403`, CertHintReach},
		{"the domain does not exist", `HTTP 400 urn:ietf:params:acme:error:dns - DNS problem: NXDOMAIN looking up A for app.example.com`, CertHintReach},
		{"port 80 is closed", `HTTP 400 urn:ietf:params:acme:error:connection - 203.0.113.7: Timeout during connect (likely firewall problem)`, CertHintReach},
		{"rate limited", `HTTP 429 urn:ietf:params:acme:error:rateLimited - too many certificates`, ""},
		{"not an ACME error", "unknown error", ""},
	}
	for _, tc := range cases {
		if got := certHint(tc.msg); got != tc.want {
			t.Errorf("%s: certHint = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCertManagerHintsAtAnUnreachablePort80(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	issuer.setErr(errors.New("HTTP 403 urn:ietf:params:acme:error:unauthorized - 203.0.113.7: Invalid response from http://naive.example.com/.well-known/acme-challenge/tok: 403"))
	m := newTestCertManager(t, issuer, 0)

	m.request(CertRequest{Domain: "naive.example.com"})
	st := waitForStatus(t, m, "naive.example.com", CertStateFailed)

	if st.Hint != CertHintReach {
		t.Errorf("hint = %q after a failure of Let's Encrypt reaching the panel, want %q", st.Hint, CertHintReach)
	}
}

// CertMagic backs a failed order off for a minute and then for longer, which is
// too slow for an admin who has just fixed the DNS record.
func TestCertManagerRetryOrdersAgainAtOnce(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	issuer.setErr(errors.New("DNS has not propagated"))
	m := newTestCertManager(t, issuer, 0)
	m.request(CertRequest{Domain: "naive.example.com"})
	waitForStatus(t, m, "naive.example.com", CertStateFailed)

	issuer.setErr(nil)
	m.retry("naive.example.com")

	st := waitForStatus(t, m, "naive.example.com", CertStateObtained)
	if st.Error != "" {
		t.Errorf("error = %q after a successful retry", st.Error)
	}
}

func TestCertManagerRenewsBeforeTheCertificateExpires(t *testing.T) {
	issuer := newFakeIssuer(t, 8*time.Second)
	m := newRenewingCertManager(t, issuer)
	m.request(CertRequest{Domain: "naive.example.com"})

	var first string
	waitUntil(t, "the first certificate", func() bool {
		certFile, keyFile, ok := m.lookup("naive.example.com")
		first = certDigest(certFile, keyFile)
		return ok
	})
	waitUntil(t, "a renewed certificate", func() bool {
		certFile, keyFile, ok := m.lookup("naive.example.com")
		return ok && certDigest(certFile, keyFile) != first
	})
	if got := issuer.callCount(); got < 2 {
		t.Errorf("the CA was asked %d times, want at least 2 (order and renewal)", got)
	}
}

func TestCertManagerStopsRenewingADroppedDomain(t *testing.T) {
	issuer := newFakeIssuer(t, 8*time.Second)
	m := newRenewingCertManager(t, issuer)
	m.request(CertRequest{Domain: "naive.example.com"})
	waitForStatus(t, m, "naive.example.com", CertStateObtained)

	m.retain(map[string]struct{}{})
	calls := issuer.callCount()
	// The renewal window opens after about 1.6 s, so this outlasts the renewal a kept domain would get.
	time.Sleep(2500 * time.Millisecond)

	if got := issuer.callCount(); got != calls {
		t.Errorf("the CA was asked %d more times after the domain was dropped", got-calls)
	}
	if st := m.status("naive.example.com"); st.State != CertStateObtained {
		t.Errorf("state = %q, want the issued certificate still reported as obtained", st.State)
	}
}

func TestCertManagerMovesADomainToAnotherAccount(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	m := newTestCertManager(t, issuer, 0)
	m.request(CertRequest{Domain: "naive.example.com", Email: "old@example.com"})
	waitForStatus(t, m, "naive.example.com", CertStateObtained)
	calls := issuer.callCount()

	m.request(CertRequest{Domain: "naive.example.com", Email: "new@example.com"})

	m.mu.Lock()
	email := m.managed["naive.example.com"].email
	m.mu.Unlock()
	if email != "new@example.com" {
		t.Errorf("managed under %q, want the new account", email)
	}
	waitForStatus(t, m, "naive.example.com", CertStateObtained)
	time.Sleep(200 * time.Millisecond)
	if got := issuer.callCount(); got != calls {
		t.Errorf("the CA was asked %d times, want the stored certificate reused", got)
	}
}

func TestCertManagerRefusesAnInvalidDomainWithoutAskingTheCA(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	m := newTestCertManager(t, issuer, 0)

	m.request(CertRequest{Domain: "203.0.113.7"})

	st := m.status("203.0.113.7")
	if st.State != CertStateFailed || !strings.Contains(st.Error, "IP address") {
		t.Errorf("status = %+v, want a failure that says an IP address cannot get a certificate", st)
	}
	if got := issuer.callCount(); got != 0 {
		t.Errorf("the CA was asked %d times for an invalid domain", got)
	}
}

func TestValidCertDomain(t *testing.T) {
	for _, tc := range []struct {
		domain  string
		wantErr string // "" means valid
	}{
		{"naive.example.com", ""},
		{"  Naive.Example.COM. ", ""},
		{"a-b.c1.example.org", ""},
		{"", "required"},
		{"example", "fully qualified"},
		{"203.0.113.7", "IP address"},
		{"2001:db8::1", "IP address"},
		{"*.example.com", "wildcard"},
		{"-bad.example.com", "not a valid domain"},
		{"bad-.example.com", "not a valid domain"},
		{"under_score.example.com", "not a valid domain"},
		{"пример.рф", "xn--"},
		{"exa mple.com", "not a valid domain"},
		{"https://naive.example.com", "not a valid domain"},
		{"naive.example.com:443", "not a valid domain"},
		{strings.Repeat("a", 64) + ".example.com", "not a valid domain"},
	} {
		err := ValidCertDomain(tc.domain)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("ValidCertDomain(%q) = %v, want it accepted", tc.domain, err)
		case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
			t.Errorf("ValidCertDomain(%q) = %v, want an error mentioning %q", tc.domain, err, tc.wantErr)
		}
	}
}

func TestManualCertStatus(t *testing.T) {
	certFile, _ := writeSelfSignedCert(t)

	st := ManualCertStatus(certFile)
	if st.Mode != CertManual || st.State != CertStateObtained || st.NotAfter == nil || time.Until(*st.NotAfter) <= 0 {
		t.Errorf("status of a valid file = %+v, want manual and obtained with an expiry ahead", st)
	}

	garbage := filepath.Join(t.TempDir(), "cert.pem")
	if err := os.WriteFile(garbage, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"a missing file":     filepath.Join(t.TempDir(), "absent.pem"),
		"a file with no PEM": garbage,
		"no path at all":     "",
	} {
		if st := ManualCertStatus(path); st.State != CertStateFailed || st.Error == "" || st.NotAfter != nil {
			t.Errorf("status of %s = %+v, want a failure with a reason and no expiry", name, st)
		}
	}
}

func TestCertDigestFollowsTheFiles(t *testing.T) {
	certFile, keyFile := writeSelfSignedCert(t)
	first := certDigest(certFile, keyFile)
	if first == "" || first != certDigest(certFile, keyFile) {
		t.Fatalf("digest = %q, want a stable fingerprint", first)
	}

	otherCert, otherKey := writeSelfSignedCert(t)
	if certDigest(otherCert, otherKey) == first {
		t.Error("another pair has the same digest")
	}
	// A deploy hook replaces the files one by one: the half-replaced pair is no pair, not a new one.
	if got := certDigest(certFile, otherKey); got != "" {
		t.Errorf("a certificate with another pair's key has digest %q, want none", got)
	}
	if got := certDigest(otherCert, keyFile); got != "" {
		t.Errorf("another pair's certificate with this key has digest %q, want none", got)
	}
	if certDigest(certFile, filepath.Join(t.TempDir(), "absent.key")) != "" || certDigest("", keyFile) != "" {
		t.Error("an unreadable or unset file still produced a digest")
	}
}

// CertMagic writes the key and the certificate one after the other, so for a moment they can
// belong to different orders: that pair must not be handed to Caddy.
func TestCertManagerLookupNeedsAMatchingPair(t *testing.T) {
	issuer := newFakeIssuer(t, time.Hour)
	m := newTestCertManager(t, issuer, 0)
	m.request(CertRequest{Domain: "naive.example.com"})
	waitForStatus(t, m, "naive.example.com", CertStateObtained)
	_, keyFile, _ := m.lookup("naive.example.com")

	_, otherKey := writeSelfSignedCert(t)
	data, err := os.ReadFile(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, _, ok := m.lookup("naive.example.com"); ok {
		t.Error("lookup accepted a certificate with another order's key")
	}
}
