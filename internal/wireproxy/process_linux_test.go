package wireproxy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction"
)

// withShortGrace shortens the wait between SIGTERM and SIGKILL for the test.
func withShortGrace(t *testing.T) {
	t.Helper()
	real := killGrace
	t.Cleanup(func() { killGrace = real })
	killGrace = 300 * time.Millisecond
}

// running reports whether the process exists and is not a zombie waiting to be reaped.
func running(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	// The state follows the command name, which is in parentheses and may hold spaces.
	rest := string(stat[strings.LastIndex(string(stat), ")")+1:])
	fields := strings.Fields(rest)
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// cancelWhenThere cancels the context once the file exists, so a test acts while the script is demonstrably running.
func cancelWhenThere(path string, cancel context.CancelFunc) {
	go func() {
		for range 500 {
			if _, err := os.Stat(path); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
}

// withManagerScript makes script the manager the package runs, and returns a folder for the script to write into.
func withManagerScript(t *testing.T, script string) (scratch string) {
	t.Helper()
	useHostActions(t)
	dir := t.TempDir()
	t.Setenv("WIREPROXY_TEST_DIR", dir)
	real := managerBinPath
	t.Cleanup(func() { managerBinPath = real })
	managerBinPath = filepath.Join(dir, "warpwp")
	if err := os.WriteFile(managerBinPath, []byte("#!/usr/bin/env bash\n"+script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}
	return dir
}

// A status that hangs starts probes of its own. Killing only bash left them running and kept the call waiting on the
// pipes they hold; the whole group has to go, even a child that ignores SIGTERM.
func TestStatusStopsTheWholeGroupWhenTheContextEnds(t *testing.T) {
	withShortGrace(t)
	dir := withManagerScript(t, "trap '' TERM\nsleep 300 &\necho $! > \"$WIREPROXY_TEST_DIR/child\"\nwait\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(filepath.Join(dir, "child"), cancel)

	started := time.Now()
	_, err := runLocal(ctx, actionStatusJSON)

	raw, readErr := os.ReadFile(filepath.Join(dir, "child"))
	if readErr != nil {
		t.Fatalf("the script never started its child: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatalf("the child's pid %q: %v", raw, convErr)
	}
	for range 200 {
		if !running(pid) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err == nil || time.Since(started) > 10*time.Second || running(pid) {
		t.Errorf("runLocal = %v after %v, child %d running: %v; want an error, a prompt return and the child gone", err, time.Since(started), pid, running(pid))
	}
}

// SIGTERM comes first, so the script's own traps can clean up before anything is killed.
func TestStatusAsksTheScriptToStopBeforeKillingIt(t *testing.T) {
	withShortGrace(t)
	dir := withManagerScript(t, "trap 'echo cleaned > \"$WIREPROXY_TEST_DIR/cleaned\"; exit 0' TERM\necho ready > \"$WIREPROXY_TEST_DIR/ready\"\nwhile :; do sleep 0.05; done\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(filepath.Join(dir, "ready"), cancel)

	_, _ = runLocal(ctx, actionStatusJSON)

	if _, err := os.Stat(filepath.Join(dir, "cleaned")); err != nil {
		t.Errorf("the script was killed before its TERM trap could run: %v", err)
	}
}

// The modal polls the status, which writes nothing: it must not start a unit of its own for every poll.
func TestStatusRunsAsAChildOfThePanelNotAsAUnit(t *testing.T) {
	dir := withManagerScript(t, "echo \"${HOSTACTION_DIR-none}\" > \"$WIREPROXY_TEST_DIR/where\"\necho '{}'\n")

	if _, err := GetStatus(context.Background()); err != nil {
		t.Fatalf("GetStatus: %v", err)
	}

	if where, _ := os.ReadFile(filepath.Join(dir, "where")); strings.TrimSpace(string(where)) != "none" {
		t.Errorf("the status ran with the host action folder %q, want it run by the panel itself", where)
	}
}

// A scan only rewrites what the next scan rebuilds, so it is stopped with everything it started when its time is up.
func TestAScanIsStoppedWithEverythingItStartedWhenItsTimeIsUp(t *testing.T) {
	dir := withManagerScript(t, "trap 'exit 0' TERM\nsleep 300 &\necho $! > \"$WIREPROXY_TEST_DIR/child\"\nwait\n")
	realNative := nativeBinPath
	t.Cleanup(func() { nativeBinPath = realNative })
	nativeBinPath = filepath.Join(dir, "native.sh")
	if err := os.WriteFile(nativeBinPath, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(filepath.Join(dir, "child"), cancel)

	err := Repair(ctx, RepairQuickScan)

	raw, readErr := os.ReadFile(filepath.Join(dir, "child"))
	if readErr != nil {
		t.Fatalf("the scan never started its child: %v", readErr)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	var detached *hostaction.DetachedError
	if !errors.Is(err, context.Canceled) || errors.As(err, &detached) || running(pid) {
		t.Errorf("Repair = %v, child %d running: %v; want the context error, no leftover and the child gone", err, pid, running(pid))
	}
}

// An install, a removal and the routing cleanup change the host, and a package manager killed halfway leaves its
// database interrupted: when their time is up they are left to finish.
func TestAnInstallIsLeftToFinishWhenItsTimeIsUp(t *testing.T) {
	record := withEmbeddedStandIns(t)
	dir := filepath.Dir(record)
	t.Setenv("WIREPROXY_TEST_DIR", dir)
	// Created when the test ends, so that a test that fails halfway leaves no script spinning behind.
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "release"), nil, 0o600) })
	managerScript = []byte("#!/usr/bin/env bash\necho ready > \"$WIREPROXY_TEST_DIR/ready\"\nwhile [ ! -e \"$WIREPROXY_TEST_DIR/release\" ]; do sleep 0.05; done\necho finished > \"$WIREPROXY_TEST_DIR/finished\"\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(filepath.Join(dir, "ready"), cancel)

	err := Install(ctx)

	var detached *hostaction.DetachedError
	if !errors.As(err, &detached) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want a DetachedError wrapping the context error: the install goes on", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "finished")); statErr == nil {
		t.Fatal("Install returned only after the manager had finished")
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 500 {
		if _, statErr := os.Stat(filepath.Join(dir, "finished")); statErr == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the manager never finished: it was stopped when the context of the install ended")
}
