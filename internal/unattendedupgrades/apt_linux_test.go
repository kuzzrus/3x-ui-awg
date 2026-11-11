//go:build linux

package unattendedupgrades

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

// fakeAptGet records what it is asked and the prompt setting it was started with. What it does besides is up to files a
// test leaves beside it, since a unit starts without the test's settings.
const fakeAptGet = `#!/bin/bash
dir=$(dirname "$0")
echo "$* [frontend=${DEBIAN_FRONTEND-unset}]" >> "$dir/calls"
if [ "$1" = update ] && [ -e "$dir/update-fails" ]; then echo "E: Failed to fetch the package index" >&2; exit 100; fi
exit 0
`

// fakeUnattendedUpgrade prints on both streams and can hold on until the test lets it go, or until its folder is gone.
const fakeUnattendedUpgrade = `#!/bin/bash
dir=$(dirname "$0")
echo "Starting unattended upgrades script"
echo "W: a warning on stderr" >&2
echo "frontend=${DEBIAN_FRONTEND-unset}"
if [ -e "$dir/stall" ]; then
  echo ready > "$dir/ready"
  while [ ! -e "$dir/release" ] && [ -d "$dir" ]; do sleep 0.05; done
  echo finished > "$dir/finished"
fi
exit "$(cat "$dir/exit-status" 2>/dev/null || echo 0)"
`

// withFakeApt puts the stand-ins first on PATH, makes commands run as children of the test, and returns their folder.
func withFakeApt(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"apt-get": fakeAptGet, "unattended-upgrade": fakeUnattendedUpgrade} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XUI_HOSTACTION", "direct")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	realInstalled := isInstalled
	isInstalled = func() bool { return false }
	t.Cleanup(func() { isInstalled = realInstalled })
	return dir
}

// leave makes the stand-ins do what the file's name says, with the content for the one that is read.
func leave(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func aptCalls(t *testing.T, dir string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return string(raw)
}

func waitForRun(t *testing.T) RunStatus {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if status := GetRunStatus(); status.State != RunRunning && status.State != RunPending {
			return status
		}
	}
	t.Fatal("the run never ended")
	return RunStatus{}
}

// waitForRunToEnd waits until hostaction no longer counts the run, which a panel that stopped waiting for it still does.
func waitForRunToEnd(t *testing.T) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if !hostaction.Running(runName) {
			return
		}
	}
	t.Fatal("the run never ended")
}

const installCalls = "update [frontend=noninteractive]\n" +
	"install -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold unattended-upgrades [frontend=noninteractive]\n"

func TestInstallRefreshesTheIndexThenInstallsWithoutPrompts(t *testing.T) {
	dir := withFakeApt(t)

	if err := Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if got := aptCalls(t, dir); got != installCalls {
		t.Errorf("apt-get was run as\n%s\nwant\n%s", got, installCalls)
	}
}

func TestInstallInstallsNothingAfterAFailedRefresh(t *testing.T) {
	dir := withFakeApt(t)
	leave(t, dir, "update-fails", "")

	err := Install(context.Background())

	var exit *hostaction.ExitError
	if !errors.As(err, &exit) || exit.Code != 100 || exit.Stderr != "E: Failed to fetch the package index" {
		t.Errorf("Install = %v, want an exit status 100 with what apt-get said", err)
	}
	if got := aptCalls(t, dir); got != "update [frontend=noninteractive]\n" {
		t.Errorf("apt-get was run as\n%s\nwant the refresh only", got)
	}
}

func TestRunNowKeepsEverythingTheRunPrintedInTheOrderItCame(t *testing.T) {
	withFakeApt(t)

	if _, err := RunNow(); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	status := waitForRun(t)

	want := "Starting unattended upgrades script\nW: a warning on stderr\nfrontend=noninteractive\n"
	if status.State != RunSuccess || status.Tail != want {
		t.Errorf("the run ended as %q with output %q, want success with %q", status.State, status.Tail, want)
	}
	if log, err := os.ReadFile(LogPath()); err != nil || string(log) != want {
		t.Errorf("the log holds %q (%v), want %q", log, err, want)
	}
}

func TestRunNowRecordsAFailedRunWithItsOutputAndExitStatus(t *testing.T) {
	dir := withFakeApt(t)
	leave(t, dir, "exit-status", "3")

	if _, err := RunNow(); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	status := waitForRun(t)

	if status.State != RunFailed || !strings.HasPrefix(status.Tail, "Starting unattended upgrades script\n") || !strings.HasSuffix(status.Tail, "unattended-upgrade -v: exit status 3") {
		t.Errorf("the run ended as %q with output %q, want a failure with its output and exit status", status.State, status.Tail)
	}
}

// Stopping apt halfway leaves dpkg unfinished, so a run that outlives the wait is left to finish, and no second run may
// start beside it. The record says the run goes on until nothing runs any more.
func TestARunThatOutlivesTheWaitIsLeftToFinishAndNoSecondRunStartsBesideIt(t *testing.T) {
	dir := withFakeApt(t)
	leave(t, dir, "stall", "")
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

	execute(ctx, "run-1", time.Now().Add(-2*time.Minute).Unix())

	if status := GetRunStatus(); status.State != RunRunning || !strings.Contains(status.Tail, "left the run to finish") {
		t.Fatalf("the run is recorded as %q with %q, want one that goes on and says it was left to finish", status.State, status.Tail)
	}
	if _, err := RunNow(); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Errorf("RunNow beside a run that goes on = %v, want it to refuse", err)
	}
	leave(t, dir, "release", "")
	waitForRunToEnd(t)
	if _, err := os.Stat(filepath.Join(dir, "finished")); err != nil {
		t.Error("the run never finished: it was stopped when the panel stopped waiting for it")
	}
	if status := GetRunStatus(); status.State != RunFailed || !strings.Contains(status.Tail, "the panel log says how it ended") {
		t.Errorf("after the run ended it is recorded as %q with %q, want a failure that sends to the panel log", status.State, status.Tail)
	}
	if _, err := RunNow(); err != nil {
		t.Fatalf("RunNow after the run ended: %v", err)
	}
	waitForRun(t)
}

// No test can reproduce the sandbox, but its way out can be seen: apt-get, the settings files and the run itself all go
// through units, which are handed the setting they need because a unit starts without the panel's environment.
func TestEverythingThatChangesTheHostRunsAsAUnit(t *testing.T) {
	withTempAptPaths(t)
	dir := withFakeApt(t)
	fake := hostactiontest.Use(t, "255")
	ctx := context.Background()

	if err := Install(ctx); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Configure(ctx, ModeFull, true); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := Disable(ctx); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	execute(ctx, "run-1", time.Now().Unix())

	units := fake.Units()
	prefixes := []string{
		"x-ui-hostaction-apt-get-update-", "x-ui-hostaction-apt-get-install-y-o-",
		"x-ui-hostaction-write-", "x-ui-hostaction-write-",
		"x-ui-hostaction-write-", "x-ui-hostaction-remove-",
		"x-ui-hostaction-unattended-upgrade-v-",
	}
	if len(units) != len(prefixes) {
		t.Fatalf("%d units were started, want %d: %v", len(units), len(prefixes), units)
	}
	for i, prefix := range prefixes {
		if !strings.HasPrefix(units[i], prefix) {
			t.Errorf("unit %d is %q, want one that starts with %q", i, units[i], prefix)
		}
	}
	if got := aptCalls(t, dir); got != installCalls {
		t.Errorf("apt-get was run as\n%s\nwant\n%s", got, installCalls)
	}
	if status := GetRunStatus(); status.State != RunSuccess || !strings.HasSuffix(status.Tail, "frontend=noninteractive\n") {
		t.Errorf("the run ended as %q with output %q, want success and the setting it needs", status.State, status.Tail)
	}
}
