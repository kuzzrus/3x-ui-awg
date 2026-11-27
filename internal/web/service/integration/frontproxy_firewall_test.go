package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/dbtest"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

type recordingOpener struct {
	ports    []int
	released int
}

func (r *recordingOpener) Open(_ context.Context, port int) (func(), error) {
	r.ports = append(r.ports, port)
	return func() { r.released++ }, nil
}

func (r *recordingOpener) Sweep(context.Context) {}

func useOpener(t *testing.T) *recordingOpener {
	t.Helper()
	rec := &recordingOpener{}
	service.SetAcmeFirewall(rec)
	t.Cleanup(func() { service.SetAcmeFirewall(nil) })
	return rec
}

// The certificate half of the reverse proxy is given the firewall, and the setting is read at each round, not once when the reverse
// proxy starts: a switch that is turned off takes effect at the next round, and one that is turned on, too.
func TestFrontProxyTLSSettingsLetThePortThroughWhileTheSwitchIsOn(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	rec := useOpener(t)
	svc := &FrontProxyService{}
	tls, err := svc.tlsSettings()
	if err != nil {
		t.Fatalf("tlsSettings: %v", err)
	}
	if tls.Firewall == nil {
		t.Fatal("the certificate settings carry no firewall")
	}

	release, err := tls.Firewall.Open(context.Background(), 80)
	if err != nil || len(rec.ports) != 1 || rec.ports[0] != 80 {
		t.Fatalf("with the switch at its default: error %v, ports opened %v, want 80 opened", err, rec.ports)
	}
	release()
	if rec.released != 1 {
		t.Fatalf("the release of the opener was not the one returned: %d", rec.released)
	}

	setSetting(t, "frontProxyAcmeFirewall", "false")
	release, err = tls.Firewall.Open(context.Background(), 80)
	release()
	if err != nil || len(rec.ports) != 1 {
		t.Fatalf("with the switch off: error %v, ports opened %v, want nothing more opened", err, rec.ports)
	}

	setSetting(t, "frontProxyAcmeFirewall", "true")
	if _, err := tls.Firewall.Open(context.Background(), 80); err != nil || len(rec.ports) != 2 {
		t.Fatalf("with the switch on again: error %v, ports opened %v, want the port opened", err, rec.ports)
	}
}

func TestTheSwitchIsOnByDefault(t *testing.T) {
	dbtest.InitDB(t, filepath.Join(t.TempDir(), "x-ui.db"))
	on, err := (&FrontProxyService{}).GetFrontProxyAcmeFirewall()
	if err != nil || !on {
		t.Fatalf("GetFrontProxyAcmeFirewall = %v, %v, want on by default", on, err)
	}
}

func TestAnUnreadableSwitchIsAnErrorAndLetsNothingThrough(t *testing.T) {
	rec := useOpener(t)
	release, err := (acmeFirewall{enabled: func() (bool, error) { return true, errors.New("no database") }}).Open(context.Background(), 80)
	release()
	if err == nil || len(rec.ports) != 0 {
		t.Fatalf("error %v, ports opened %v, want the failure and no hole", err, rec.ports)
	}
}
