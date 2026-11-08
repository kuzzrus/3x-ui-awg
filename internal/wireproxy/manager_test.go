package wireproxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

func readRecord(t *testing.T, path string) string {
	t.Helper()
	out, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(out)
}

// withEmbeddedStandIns swaps the embedded scripts for stand-ins and the paths Install writes them to for a temp dir.
// The manager stand-in records the arguments it is run with in the returned file.
func withEmbeddedStandIns(t *testing.T) (record string) {
	t.Helper()
	dir := t.TempDir()
	record = filepath.Join(dir, "args")
	t.Setenv("WIREPROXY_TEST_RECORD", record)
	realManager, realNative, realManagerBin, realNativeBin := managerScript, nativeScript, managerBinPath, nativeBinPath
	t.Cleanup(func() {
		managerScript, nativeScript, managerBinPath, nativeBinPath = realManager, realNative, realManagerBin, realNativeBin
	})
	managerScript = []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$@\" > \"$WIREPROXY_TEST_RECORD\"\n")
	nativeScript = []byte("#!/usr/bin/env bash\n# the native script\n")
	managerBinPath = filepath.Join(dir, "warpwp")
	nativeBinPath = filepath.Join(dir, "warp-wireproxy-native.sh")
	return record
}

// Install runs the manager with the action that fetches no script, and leaves both scripts where warpwp expects them.
func TestInstallPlacesTheScriptsAndRunsInstallCurrent(t *testing.T) {
	record := withEmbeddedStandIns(t)

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := strings.TrimSpace(readRecord(t, record)); got != "--install-current" {
		t.Errorf("the manager was run with %q, want --install-current", got)
	}
	if native, err := os.ReadFile(nativeBinPath); err != nil || string(native) != string(nativeScript) {
		t.Errorf("the native script was not placed as embedded (%q, %v)", native, err)
	}
}

// An older copy that lost its executable bit is replaced by one that has it: warpwp runs the native script directly,
// not through bash.
func TestInstallMakesAnOlderScriptExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes mean nothing on Windows")
	}
	withEmbeddedStandIns(t)
	for _, path := range []string{managerBinPath, nativeBinPath} {
		if err := os.WriteFile(path, []byte("an older copy\n"), 0o644); err != nil {
			t.Fatalf("seeding %s: %v", path, err)
		}
	}

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	for _, path := range []string{managerBinPath, nativeBinPath} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s is not executable after Install (%v, %v)", path, info, err)
		}
	}
}

// The scheduler runs $NATIVE_BIN directly every 10 minutes and bash reads a script as it executes it, so Install has to
// replace the file rather than rewrite the one a check may be running.
func TestInstallReplacesTheScriptTheSchedulerMayBeRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a file that is open cannot be replaced on Windows")
	}
	withEmbeddedStandIns(t)
	const running = "the copy a scheduled check is executing\n"
	if err := os.WriteFile(nativeBinPath, []byte(running), 0o755); err != nil {
		t.Fatalf("seeding %s: %v", nativeBinPath, err)
	}
	handle, err := os.Open(nativeBinPath)
	if err != nil {
		t.Fatalf("opening %s: %v", nativeBinPath, err)
	}
	defer handle.Close()

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	still, err := io.ReadAll(handle)
	if err != nil {
		t.Fatalf("reading the already-open copy: %v", err)
	}
	if string(still) != running {
		t.Errorf("the already-open copy now reads %q; Install rewrote the file bash is executing", still)
	}
}

// A placement that fails leaves no staged copy next to the target.
func TestInstallLeavesNoStagedCopyWhenPlacingFails(t *testing.T) {
	withEmbeddedStandIns(t)
	if err := os.Mkdir(nativeBinPath, 0o755); err != nil {
		t.Fatalf("making a directory where the native script goes: %v", err)
	}

	if err := Install(context.Background()); err == nil {
		t.Fatal("Install succeeded although the native script's path is a directory")
	}

	entries, err := os.ReadDir(filepath.Dir(nativeBinPath))
	if err != nil {
		t.Fatalf("listing %s: %v", filepath.Dir(nativeBinPath), err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".new.") {
			t.Errorf("a staged copy was left behind: %s", entry.Name())
		}
	}
}

// A stand-in for warpwp that runs the vendored manager's own code with everything that would touch the host replaced by
// a line in $WIREPROXY_TEST_RECORD, so a test sees what the real script does for the arguments the panel sends.
const sandboxedManager = `#!/usr/bin/env bash
source "$WIREPROXY_TEST_MANAGER"
MANAGER_BIN=/nonexistent/warpwp
NATIVE_BIN=/nonexistent/warp-wireproxy-native.sh
need_root() { :; }
acquire_admin_lock() { :; }
acquire_maintenance_locks() { :; }
release_maintenance_locks() { :; }
remove_timer_check_quiet() { :; }
remove_cron_check() { :; }
remove_project_wg_files() { :; }
systemctl() { echo "systemctl $*" >> "$WIREPROXY_TEST_RECORD"; [[ "${1:-}" != is-* ]]; }
ip() { echo "ip $*" >> "$WIREPROXY_TEST_RECORD"; return 1; }
rm() { echo "rm $*" >> "$WIREPROXY_TEST_RECORD"; }
rmdir() { echo "rmdir $*" >> "$WIREPROXY_TEST_RECORD"; return 1; }
`

// sandboxedManagerRun ends the stand-in; a test adds its own overrides before it.
const sandboxedManagerRun = "main \"$@\"\n"

// withSandboxedManager makes the sandboxed stand-in, with the overrides a test adds, the manager the package runs, and
// returns the record it writes.
func withSandboxedManager(t *testing.T, overrides ...string) (record string) {
	t.Helper()
	dir := t.TempDir()
	manager := filepath.Join(dir, "warpwp.sh")
	if err := os.WriteFile(manager, managerScript, 0o644); err != nil {
		t.Fatalf("writing the vendored manager: %v", err)
	}
	record = filepath.Join(dir, "record")
	t.Setenv("WIREPROXY_TEST_MANAGER", manager)
	t.Setenv("WIREPROXY_TEST_RECORD", record)
	t.Setenv("WG_DIR", filepath.Join(dir, "wireguard"))
	withFakeManagerBin(t, false)
	script := sandboxedManager + strings.Join(overrides, "") + sandboxedManagerRun
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the sandboxed manager: %v", err)
	}
	return record
}

// The manager (v1.3.9) removes the system WARP routing only when told --force and otherwise warns and succeeds, which
// would leave the panel's fix button reporting a cleanup that never happened.
func TestFixRoutingClearsTheSystemWarpRouting(t *testing.T) {
	record := withSandboxedManager(t)

	if err := FixRouting(context.Background()); err != nil {
		t.Fatalf("FixRouting: %v", err)
	}

	if got := readRecord(t, record); !strings.Contains(got, "systemctl disable --now wg-quick@warp wg-quick@wgcf warp-svc\n") {
		t.Errorf("the manager did not remove the system WARP routing; it ran:\n%s", got)
	}
}

// --install-current is the half of --install that fetches nothing; plain --install replaces both scripts with the latest
// release first, so a panel build would run a manager it never shipped.
func TestTheInstallActionFetchesNoScript(t *testing.T) {
	native := filepath.Join(t.TempDir(), "warp-wireproxy-native.sh")
	if err := os.WriteFile(native, []byte("#!/usr/bin/env bash\necho native >> \"$WIREPROXY_TEST_RECORD\"\n"), 0o755); err != nil {
		t.Fatalf("writing the native stand-in: %v", err)
	}
	record := withSandboxedManager(t,
		"update_local_scripts() { echo update_local_scripts >> \"$WIREPROXY_TEST_RECORD\"; }\n",
		"install_cron_check() { echo install_cron_check >> \"$WIREPROXY_TEST_RECORD\"; }\n",
		"NATIVE_BIN=\""+native+"\"\n",
	)

	_, err := run(context.Background(), actionInstall)

	got := readRecord(t, record)
	if strings.Contains(got, "update_local_scripts") {
		t.Fatalf("the install action fetched the scripts from the network; it ran:\n%s", got)
	}
	if err != nil {
		t.Fatalf("the install action: %v", err)
	}
	if !strings.Contains(got, "native\n") {
		t.Errorf("the install action never ran the native installer; it ran:\n%s", got)
	}
}

// --remove asks y/N on stdin, and the script's set -e aborts at the read when nothing answers.
func TestUninstallStopsTheService(t *testing.T) {
	record := withSandboxedManager(t)

	if err := Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if got := readRecord(t, record); !strings.Contains(got, "systemctl stop wireproxy\n") {
		t.Errorf("the manager never reached the removal; it ran:\n%s", got)
	}
}

// After Uninstall the native script is gone, and warpwp answers a scan without it by replacing both scripts with the
// latest release -- the one panel action left that could run a manager this build never shipped.
func TestRepairRefusesToLetWarpwpFetchTheScripts(t *testing.T) {
	for _, tc := range []struct {
		name string
		have bool
	}{
		{"missing", false},
		{"not executable", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := withSandboxedManager(t,
				"update_local_scripts() { echo update_local_scripts >> \"$WIREPROXY_TEST_RECORD\"; return 1; }\n",
			)
			realNativeBin := nativeBinPath
			t.Cleanup(func() { nativeBinPath = realNativeBin })
			nativeBinPath = filepath.Join(t.TempDir(), "warp-wireproxy-native.sh")
			if tc.have {
				if err := os.WriteFile(nativeBinPath, []byte("#!/usr/bin/env bash\n"), 0o644); err != nil {
					t.Fatalf("placing the native script: %v", err)
				}
			}

			err := Repair(context.Background(), RepairCheck)

			if got := readRecord(t, record); strings.Contains(got, "update_local_scripts") {
				t.Errorf("the repair action had warpwp fetch the scripts; it ran:\n%s", got)
			}
			if err == nil || !strings.Contains(err.Error(), nativeBinPath) {
				t.Errorf("Repair error = %v, want it to name %s", err, nativeBinPath)
			}
		})
	}
}

// With the native script in place Repair runs the scan its mode names.
func TestRepairRunsTheScanWhenTheNativeScriptIsPlaced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes mean nothing on Windows")
	}
	record := withEmbeddedStandIns(t)
	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if err := Repair(context.Background(), RepairDeepScan); err != nil {
		t.Fatalf("Repair: %v", err)
	}

	if got := strings.TrimSpace(readRecord(t, record)); got != "--deep-scan" {
		t.Errorf("the manager was run with %q, want --deep-scan", got)
	}
}
