package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/naiveproxy"
)

const fakeNaiveProxyBinary = "pretend-binary"

func placeFakeNaiveProxyBinary(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(naiveproxy.Dir(), 0o700); err != nil {
		t.Fatalf("create %s: %v", naiveproxy.Dir(), err)
	}
	if err := os.WriteFile(naiveproxy.BinPath(), []byte(fakeNaiveProxyBinary), 0o750); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
}

func TestNaiveProxyStatusFollowsTheBinary(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	s := &NaiveProxyService{}

	got := s.Status()
	if got.Installed {
		t.Fatal("Installed = true with an empty bin folder")
	}
	if want := runtime.GOOS + "/" + runtime.GOARCH; got.Platform != want {
		t.Fatalf("Platform = %q, want %q", got.Platform, want)
	}
	if want := runtime.GOOS == "linux" && runtime.GOARCH == "amd64"; got.Supported != want {
		t.Fatalf("Supported = %v, want %v on %s", got.Supported, want, got.Platform)
	}

	placeFakeNaiveProxyBinary(t)
	if !s.Status().Installed {
		t.Fatal("Installed = false right after the binary was placed")
	}
}

// A download would replace the placeholder with the real engine (or fail
// offline), so an untouched file proves Install returned before fetching anything.
func TestNaiveProxyInstallIsANoOpWhenInstalled(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	placeFakeNaiveProxyBinary(t)

	if err := (&NaiveProxyService{}).Install(); err != nil {
		t.Fatalf("Install with the engine already present: %v", err)
	}
	got, err := os.ReadFile(naiveproxy.BinPath())
	if err != nil {
		t.Fatalf("read the binary back: %v", err)
	}
	if string(got) != fakeNaiveProxyBinary {
		t.Fatalf("Install replaced the engine that was already installed (%d bytes now)", len(got))
	}
}

// A disabled automatic inbound is never ordered, so its idle state means "off", not "about to be
// ordered": the form needs to know which inbounds are enabled.
func TestNaiveProxyCertsReportsWhichInboundsAreEnabled(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	db := database.GetDB()
	for _, in := range []model.Inbound{
		{Tag: "np-on", Port: 46501, Protocol: model.NaiveProxy, Enable: true, Settings: `{"domain":"on.example.com","certMode":"auto"}`},
		{Tag: "np-off", Port: 46502, Protocol: model.NaiveProxy, Enable: true, Settings: `{"domain":"off.example.com","certMode":"auto"}`},
	} {
		if err := db.Create(&in).Error; err != nil {
			t.Fatalf("create inbound %s: %v", in.Tag, err)
		}
	}
	// A zero Enable would be replaced by the column default on Create, so it is switched off afterwards.
	if err := db.Model(&model.Inbound{}).Where("tag = ?", "np-off").Update("enable", false).Error; err != nil {
		t.Fatalf("switch inbound off: %v", err)
	}

	certs, err := (&NaiveProxyService{}).Certs()
	if err != nil {
		t.Fatalf("Certs: %v", err)
	}
	if len(certs) != 2 {
		t.Fatalf("got %d entries, want one per NaiveProxy inbound: %+v", len(certs), certs)
	}
	if got := certs[0]; got.Domain != "on.example.com" || !got.Enable || got.Mode != naiveproxy.CertAuto {
		t.Errorf("first entry = %+v, want the enabled automatic inbound", got)
	}
	if got := certs[1]; got.Domain != "off.example.com" || got.Enable {
		t.Errorf("second entry = %+v, want the disabled inbound reported as off", got)
	}
}
