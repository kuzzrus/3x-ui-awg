package wireproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// withFakeManagerBin points managerBinPath at a throwaway file for the
// duration of the test and restores it after -- never touches the real
// /usr/local/bin/warpwp.
func withFakeManagerBin(t *testing.T, present bool) {
	t.Helper()
	real := managerBinPath
	t.Cleanup(func() { managerBinPath = real })
	managerBinPath = filepath.Join(t.TempDir(), "warpwp")
	if present {
		if err := os.WriteFile(managerBinPath, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
			t.Fatalf("seeding fake manager binary: %v", err)
		}
	}
}

func TestIsInstalledReflectsManagerBinPath(t *testing.T) {
	withFakeManagerBin(t, false)
	if IsInstalled() {
		t.Error("IsInstalled() = true before the fake manager binary was written")
	}
	if err := os.WriteFile(managerBinPath, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatalf("writing fake manager binary: %v", err)
	}
	if !IsInstalled() {
		t.Error("IsInstalled() = false after the fake manager binary was written")
	}
}

func TestStartFailsWhenNotInstalled(t *testing.T) {
	withFakeManagerBin(t, false)
	err := Start(context.Background())
	if err == nil {
		t.Fatal("Start() with no manager binary present returned nil error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("Start() error = %q, want it to say not installed", err.Error())
	}
}

func TestRepairRejectsUnknownMode(t *testing.T) {
	// No fake binary needed -- mode validation happens before Repair would
	// ever shell out, so this must fail without touching managerBinPath at all.
	withFakeManagerBin(t, false)
	err := Repair(context.Background(), RepairMode("--rm -rf /"))
	if err == nil {
		t.Fatal("Repair with an unrecognized mode returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown repair mode") {
		t.Errorf("Repair error = %q, want it to say unknown repair mode", err.Error())
	}
}

// The embedded scripts are the assets of one signed release of the manager, byte for byte. These are the digests that
// release lists for them (its signed SHA256SUMS holds the same), so a patched or truncated copy cannot pass.
func TestEmbeddedScriptsAreTheReleaseAssets(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script []byte
		want   string
	}{
		{"warpwp.sh of v1.3.9", managerScript, "36bd1c13684207515420f852474dbbdcbbbc5c0a42813bc65fe10e530a7291e8"},
		{"warp-wireproxy-native.sh of v1.3.9", nativeScript, "fdad34fa42f3b5282eca21a417e620cb8b222c7d0fafb6b99c2ddf6907b8c0ae"},
	} {
		sum := sha256.Sum256(tc.script)
		if got := hex.EncodeToString(sum[:]); got != tc.want {
			t.Errorf("%s: sha256 = %s, want %s", tc.name, got, tc.want)
		}
	}
}

// Every action the package runs has to be one the vendored manager answers to; a script that lost one would fail only
// when an admin pressed the button.
func TestEmbeddedManagerAnswersEveryActionThePanelRuns(t *testing.T) {
	actions := []string{actionInstall, actionRemove, actionStatusJSON, actionFixRouting}
	for _, mode := range []RepairMode{RepairCheck, RepairQuickScan, RepairDeepScan} {
		actions = append(actions, "--"+string(mode))
	}
	for _, action := range actions {
		if !regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(action) + `[|)]`).Match(managerScript) {
			t.Errorf("the manager script has no case for %s", action)
		}
	}
}

// Install runs the manager with the action that fetches no script, and leaves both scripts where warpwp expects them.
func TestInstallPlacesTheScriptsAndRunsInstallCurrent(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "args")
	t.Setenv("WIREPROXY_TEST_RECORD", record)
	realManager, realNative, realManagerBin, realNativeBin := managerScript, nativeScript, managerBinPath, nativeBinPath
	t.Cleanup(func() {
		managerScript, nativeScript, managerBinPath, nativeBinPath = realManager, realNative, realManagerBin, realNativeBin
	})
	managerScript = []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$WIREPROXY_TEST_RECORD\"\n")
	nativeScript = []byte("#!/usr/bin/env bash\n# the native script\n")
	managerBinPath = filepath.Join(dir, "warpwp")
	nativeBinPath = filepath.Join(dir, "warp-wireproxy-native.sh")

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	args, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the manager was not run: %v", err)
	}
	if got := strings.TrimSpace(string(args)); got != "--install-current" {
		t.Errorf("the manager was run with %q, want --install-current", got)
	}
	if native, err := os.ReadFile(nativeBinPath); err != nil || string(native) != string(nativeScript) {
		t.Errorf("the native script was not placed as embedded (%q, %v)", native, err)
	}
}
