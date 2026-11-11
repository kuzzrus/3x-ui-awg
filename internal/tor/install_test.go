package tor

import (
	"context"
	"testing"
)

func TestInstallWithNoPackageManager(t *testing.T) {
	if _, ok := detectPackageManager(); ok {
		t.Skip("a real package manager is on PATH -- this case only reproduces where none is")
	}
	if IsAvailable() {
		t.Skip("tor is already on PATH, Install() would no-op before ever reaching package-manager detection")
	}
	err := Install(context.Background())
	if err == nil {
		t.Fatal("Install() with no package manager on PATH: want error, got nil")
	}
}

func TestUninstallIsANoOpWhenAlreadyAbsent(t *testing.T) {
	if IsAvailable() {
		t.Skip("tor is on PATH -- this case only reproduces where it's already absent")
	}
	if err := Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall() when tor was never installed: want nil, got %v", err)
	}
}
