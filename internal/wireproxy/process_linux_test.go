package wireproxy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
func cancelWhenThere(t *testing.T, path string, cancel context.CancelFunc) {
	t.Helper()
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

// The scripts start package managers and scans of their own. Killing only bash left them running with the locks held, and
// kept the call waiting on their pipes; the whole group has to go, even a child that ignores SIGTERM.
func TestRunStopsTheWholeGroupWhenTheContextEnds(t *testing.T) {
	withShortGrace(t)
	dir := t.TempDir()
	child := filepath.Join(dir, "child.pid")
	t.Setenv("WIREPROXY_TEST_CHILD", child)
	withFakeManagerBin(t, false)
	script := "#!/usr/bin/env bash\ntrap '' TERM\nsleep 300 &\necho $! > \"$WIREPROXY_TEST_CHILD\"\nwait\n"
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(t, child, cancel)

	started := time.Now()
	_, err := run(ctx, "--check")

	if err == nil {
		t.Error("run reported success although its context ended while the script was running")
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("run took %v to return after its context ended", took)
	}
	raw, readErr := os.ReadFile(child)
	if readErr != nil {
		t.Fatalf("the script never started its child: %v", readErr)
	}
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(raw)))
	if convErr != nil {
		t.Fatalf("the child's pid %q: %v", raw, convErr)
	}
	for range 200 {
		if !running(pid) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the child %d of the script is still running after run returned", pid)
}

// SIGTERM comes first, so the script's own traps can clean up before anything is killed.
func TestRunAsksTheScriptToStopBeforeKillingIt(t *testing.T) {
	withShortGrace(t)
	dir := t.TempDir()
	ready, mark := filepath.Join(dir, "ready"), filepath.Join(dir, "cleaned")
	t.Setenv("WIREPROXY_TEST_READY", ready)
	t.Setenv("WIREPROXY_TEST_MARK", mark)
	withFakeManagerBin(t, false)
	script := "#!/usr/bin/env bash\ntrap 'echo cleaned > \"$WIREPROXY_TEST_MARK\"; exit 0' TERM\n" +
		"echo ready > \"$WIREPROXY_TEST_READY\"\nwhile :; do sleep 0.05; done\n"
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(t, ready, cancel)

	_, _ = run(ctx, "--check")

	if _, err := os.Stat(mark); err != nil {
		t.Errorf("the script was killed before its TERM trap could run: %v", err)
	}
}
