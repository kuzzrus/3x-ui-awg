//go:build linux

package tor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction"
	"github.com/mhsanaei/3x-ui/v3/internal/hostaction/hostactiontest"
)

// fakeAptGet records what it is asked and the prompt setting it was started with, and puts a tor beside itself on
// install. What it does besides is up to files a test leaves beside it, since a unit starts without the test's settings.
const fakeAptGet = `#!/bin/bash
dir=$(dirname "$0")
echo "$* [frontend=${DEBIAN_FRONTEND-unset}]" >> "$dir/calls"
case "$1" in
  update)
    if [ -e "$dir/stall-update" ]; then
      echo ready > "$dir/ready"
      while [ ! -e "$dir/release" ]; do sleep 0.05; done
    fi
    if [ -e "$dir/update-fails" ]; then echo "E: Failed to fetch the package index" >&2; exit 100; fi ;;
  install)
    if [ -e "$dir/install-fails" ]; then echo "E: Unable to locate package tor" >&2; exit 100; fi
    if [ ! -e "$dir/leave-no-tor" ]; then printf '#!/bin/sh\n' > "$dir/tor"; chmod +x "$dir/tor"; fi ;;
  remove)
    rm -f "$dir/tor" ;;
esac
`

// withFakeAptGet puts the stand-in first on PATH, makes commands run as children of the test, and returns its folder.
func withFakeAptGet(t *testing.T) string {
	t.Helper()
	if IsAvailable() {
		t.Skip("a real tor is on PATH, so Install would have nothing to do")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "apt-get"), []byte(fakeAptGet), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XUI_HOSTACTION", "direct")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	return dir
}

// leave makes the stand-in do what the file's name says.
func leave(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func calls(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

func TestInstallRefreshesTheIndexAndInstallsWithoutPrompts(t *testing.T) {
	dir := withFakeAptGet(t)

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := "update -qq [frontend=noninteractive]\ninstall -y tor [frontend=noninteractive]\n"
	if got := calls(t, dir); got != want {
		t.Errorf("apt-get was run as\n%s\nwant\n%s", got, want)
	}
}

// No test can reproduce the sandbox, but its way out can be seen: the package manager runs as a unit, and is handed the
// setting it needs because a unit starts without the panel's environment.
func TestThePackageManagerRunsAsUnitsWithTheSettingItNeeds(t *testing.T) {
	dir := withFakeAptGet(t)
	fake := hostactiontest.Use(t, "255")

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	units := fake.Units()
	if len(units) != 2 || !strings.HasPrefix(units[0], "x-ui-hostaction-apt-get-update-qq-") || !strings.HasPrefix(units[1], "x-ui-hostaction-apt-get-install-y-tor-") {
		t.Errorf("the units were %v, want one for the refresh and one for the install", units)
	}
	want := "update -qq [frontend=noninteractive]\ninstall -y tor [frontend=noninteractive]\n"
	if got := calls(t, dir); got != want {
		t.Errorf("apt-get was run as\n%s\nwant\n%s", got, want)
	}
}

// A stale index does not stop the install: the install fails by itself if the package really cannot be found.
func TestInstallGoesOnWhenTheIndexRefreshFails(t *testing.T) {
	dir := withFakeAptGet(t)
	leave(t, dir, "update-fails")

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	want := "update -qq [frontend=noninteractive]\ninstall -y tor [frontend=noninteractive]\n"
	if got := calls(t, dir); got != want {
		t.Errorf("apt-get was run as\n%s\nwant\n%s", got, want)
	}
}

func TestInstallSaysWhyThePackageManagerFailed(t *testing.T) {
	dir := withFakeAptGet(t)
	leave(t, dir, "install-fails")

	err := Install(context.Background())

	var exit *hostaction.ExitError
	if !errors.As(err, &exit) || exit.Code != 100 || exit.Stderr != "E: Unable to locate package tor" {
		t.Errorf("Install = %v, want an exit status 100 with what apt-get said", err)
	}
}

// A refresh still running holds the package manager's lock, so an install started beside it would only fail on that.
func TestInstallStartsNothingWhileTheIndexRefreshIsStillRunning(t *testing.T) {
	dir := withFakeAptGet(t)
	leave(t, dir, "stall-update")
	t.Cleanup(func() { leave(t, dir, "release") })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for range 500 {
			if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	err := Install(ctx)

	var detached *hostaction.DetachedError
	if !errors.As(err, &detached) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want a DetachedError wrapping the context error", err)
	}
	if got := calls(t, dir); got != "update -qq [frontend=noninteractive]\n" {
		t.Errorf("apt-get was run as\n%s\nwant the refresh only", got)
	}
}

func TestInstallRefusesAPackageManagerThatLeavesNoTorBehind(t *testing.T) {
	dir := withFakeAptGet(t)
	leave(t, dir, "leave-no-tor")

	err := Install(context.Background())

	if err == nil || err.Error() != "apt-get reported success but no tor binary is on PATH afterwards" {
		t.Errorf("Install = %v, want it to say that no tor binary appeared", err)
	}
}

func TestUninstallRemovesThePackageWithoutPrompts(t *testing.T) {
	dir := withFakeAptGet(t)
	if err := os.WriteFile(filepath.Join(dir, "tor"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := Uninstall(context.Background()); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}

	if got := calls(t, dir); got != "remove -y tor [frontend=noninteractive]\n" {
		t.Errorf("apt-get was run as\n%s\nwant the removal only", got)
	}
	if IsAvailable() {
		t.Error("a tor binary is still on PATH after the removal")
	}
}
