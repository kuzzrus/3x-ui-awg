package naiveproxy

import (
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// pendingLogID keeps the inbound ids of the log-counting test apart across -count runs, since the panel log is global.
var pendingLogID atomic.Int64

func autoInst(t *testing.T, id int, domain string) Instance {
	t.Helper()
	inst := testInst(t, id, "alice")
	inst.Domain, inst.CertMode = domain, CertAuto
	return inst
}

// Until Let's Encrypt has answered there is nothing for Caddy to serve: the inbound must not
// start, and creating it must not fail, because the job starts it once the certificate lands.
func TestEnsureWaitsForAnAutomaticCertificate(t *testing.T) {
	pidFile := installFakeCaddy(t)
	issuer := newFakeIssuer(t, time.Hour)
	issuer.gate = make(chan struct{})
	m := newTestManager()
	m.certs = newTestCertManager(t, issuer, 0)
	inst := autoInst(t, 1, "naive.example.com")
	m.certs.request(CertRequest{Domain: inst.Domain})

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure while the certificate is pending = %v, want nil", err)
	}
	if got := spawnCount(t, pidFile); got != 0 {
		t.Fatalf("spawn count = %d while the certificate is pending, want 0", got)
	}

	close(issuer.gate)
	waitForStatus(t, m.certs, inst.Domain, CertStateObtained)
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure once the certificate exists: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	data, err := os.ReadFile(configPathForID(1))
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile, _ := m.certs.lookup(inst.Domain)
	if want := fmt.Sprintf("tls %s %s", caddyfileQuote(certFile), caddyfileQuote(keyFile)); !strings.Contains(string(data), want) {
		t.Errorf("Caddyfile does not point at the issued pair, want %q in:\n%s", want, data)
	}
}

// Caddy reads its certificate only at start, so a renewal that did not restart it would
// leave it serving the old one until the panel itself restarts.
func TestRenewalRestartsCaddy(t *testing.T) {
	pidFile := installFakeCaddy(t)
	issuer := newFakeIssuer(t, 8*time.Second)
	m := newTestManager()
	m.certs = newRenewingCertManager(t, issuer)
	inst := autoInst(t, 1, "naive.example.com")
	m.certs.request(CertRequest{Domain: inst.Domain})
	waitForStatus(t, m.certs, inst.Domain, CertStateObtained)

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	waitUntil(t, "Caddy to be restarted on the renewed certificate", func() bool {
		if err := m.Ensure(inst); err != nil {
			t.Fatalf("Ensure after a renewal: %v", err)
		}
		return spawnCount(t, pidFile) >= 2
	})
}

// A manual pair is replaced by the admin's own certbot, which Caddy cannot notice either.
func TestEnsureRestartsWhenAManualCertificateIsReplaced(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	certFile, keyFile := writeSelfSignedCert(t)
	inst := testInst(t, 1, "alice")
	inst.CertFile, inst.KeyFile = certFile, keyFile

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure of the same pair: %v", err)
	}
	if got := spawnCount(t, pidFile); got != 1 {
		t.Fatalf("spawn count = %d after an identical Ensure, want 1", got)
	}

	renewedCert, renewedKey := writeSelfSignedCert(t)
	copyOver(t, renewedCert, certFile)
	copyOver(t, renewedKey, keyFile)
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure after the pair was replaced: %v", err)
	}
	waitSpawnCount(t, pidFile, 2)
}

// A deploy hook writes the certificate and then the key. A Reconcile tick between the two sees
// files that do not belong together; restarting Caddy on them would take the inbound down.
func TestEnsureLeavesARunningCaddyAloneWhileAManualPairIsMismatched(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	certFile, keyFile := writeSelfSignedCert(t)
	inst := testInst(t, 1, "alice")
	inst.CertFile, inst.KeyFile = certFile, keyFile
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	renewedCert, renewedKey := writeSelfSignedCert(t)
	copyOver(t, renewedCert, certFile)
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure while the pair is half replaced: %v", err)
	}
	if got := spawnCount(t, pidFile); got != 1 {
		t.Fatalf("spawn count = %d for a mismatched pair, want the running Caddy left alone", got)
	}

	copyOver(t, renewedKey, keyFile)
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure once the pair matches again: %v", err)
	}
	waitSpawnCount(t, pidFile, 2)
}

// The pair being half replaced says nothing about the clients: removing one must still reach the
// running Caddy, or the removed client keeps getting through until the files settle.
func TestEnsureStillAppliesClientChangesWhileAManualPairIsMismatched(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	certFile, keyFile := writeSelfSignedCert(t)
	inst := testInst(t, 1, "alice")
	inst.CertFile, inst.KeyFile = certFile, keyFile
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	renewedCert, _ := writeSelfSignedCert(t)
	copyOver(t, renewedCert, certFile)
	inst.Clients = append(inst.Clients, Client{Email: "bob", Username: "bob", Password: "pw-b"})
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure with a new client and a half-replaced pair: %v", err)
	}
	waitSpawnCount(t, pidFile, 2)
}

// copyOver replaces dst with the contents of src, the way a deploy hook writes a certificate file.
func copyOver(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Waiting for a certificate is the normal first minute of an automatic inbound, so
// it is reported once and without the alarm of a failure.
func TestReconcileReportsAPendingCertificateOnceAsInfo(t *testing.T) {
	installFakeCaddy(t)
	issuer := newFakeIssuer(t, time.Hour)
	issuer.gate = make(chan struct{})
	t.Cleanup(func() { close(issuer.gate) })
	m := newTestManager()
	m.certs = newTestCertManager(t, issuer, 0)
	id := 920200 + int(pendingLogID.Add(1))
	domain := fmt.Sprintf("pending%d.example.com", id)
	inst := autoInst(t, id, domain)

	for range 3 {
		m.Reconcile([]Instance{inst})
	}

	waits := fmt.Sprintf("inbound %d waits for the certificate of %s", id, domain)
	if got := logCount("info", waits); got != 1 {
		t.Errorf("the wait was logged %d times over three ticks, want once", got)
	}
	if got := warningCount(fmt.Sprintf("reconcile failed for inbound %d:", id)); got != 0 {
		t.Errorf("a pending certificate raised %d failure warnings", got)
	}
}

func TestSyncCertsOrdersAndDropsDomains(t *testing.T) {
	m := newTestManager()
	m.SyncCerts(nil)
	if m.certs != nil {
		t.Fatal("SyncCerts with nothing to order created a certificate manager")
	}

	issuer := newFakeIssuer(t, time.Hour)
	m.certs = newTestCertManager(t, issuer, 0)
	m.SyncCerts([]CertRequest{{Domain: "one.example.com"}, {Domain: "two.example.com"}})
	waitForStatus(t, m.certs, "one.example.com", CertStateObtained)
	waitForStatus(t, m.certs, "two.example.com", CertStateObtained)

	m.SyncCerts([]CertRequest{{Domain: "one.example.com"}})

	m.certs.mu.Lock()
	_, oneManaged := m.certs.managed["one.example.com"]
	_, twoManaged := m.certs.managed["two.example.com"]
	m.certs.mu.Unlock()
	if !oneManaged || twoManaged {
		t.Errorf("managed one=%v two=%v after dropping two, want true/false", oneManaged, twoManaged)
	}
	if st := m.AutoCertStatus("two.example.com"); st.State != CertStateObtained {
		t.Errorf("a dropped domain reports %q, want its issued certificate still shown as obtained", st.State)
	}
}

// The save-time check accepts every spelling of a name, so inbounds can disagree on the case, a
// trailing dot or a space; each tick the later ones must not move the domain to their own account.
func TestSyncCertsTreatsSpellingsOfOneDomainAsOne(t *testing.T) {
	m := newTestManager()
	m.certs = newTestCertManager(t, newFakeIssuer(t, time.Hour), 0)
	reqs := []CertRequest{
		{Domain: "Naive.Example.com", Email: "first@example.com"},
		{Domain: "naive.example.com.", Email: "second@example.com"},
		{Domain: " naive.example.com", Email: ""},
	}

	for range 3 {
		m.SyncCerts(reqs)
	}
	waitForStatus(t, m.certs, "naive.example.com", CertStateObtained)

	m.certs.mu.Lock()
	managed := len(m.certs.managed)
	email := m.certs.managed["naive.example.com"].email
	m.certs.mu.Unlock()
	if managed != 1 || email != "first@example.com" {
		t.Errorf("managed %d domains, account %q, want one domain left with the first request's account", managed, email)
	}
}

func TestStopAllEndsTheCertificateManager(t *testing.T) {
	m := newTestManager()
	cm := newTestCertManager(t, newFakeIssuer(t, time.Hour), 0)
	cm.request(CertRequest{Domain: "naive.example.com"})
	waitForStatus(t, cm, "naive.example.com", CertStateObtained)
	m.certs = cm

	m.StopAll()

	if m.certs != nil {
		t.Error("StopAll left the certificate manager in place")
	}
	cm.mu.Lock()
	cache, left := cm.cache, len(cm.managed)
	cm.mu.Unlock()
	if cache != nil || left != 0 {
		t.Errorf("after StopAll cache=%v managed=%d, want everything stopped", cache, left)
	}
}

// logCount counts the panel-log entries at or above level that contain needle.
func logCount(level, needle string) int {
	n := 0
	for _, line := range logger.GetLogs(1000, level) {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}
