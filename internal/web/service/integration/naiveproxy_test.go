package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
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
