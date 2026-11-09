package wireproxy

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
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

// A scan starts probes of its own. Killing only bash left them running with the locks held, and kept the call waiting on
// the pipes they hold; the whole group has to go, even a child that ignores SIGTERM.
func TestRunStopsTheWholeGroupOfAScanWhenTheContextEnds(t *testing.T) {
	for _, action := range []string{actionStatusJSON, "--check", "--quick-scan", "--deep-scan"} {
		t.Run(action, func(t *testing.T) {
			withShortGrace(t)
			child := filepath.Join(t.TempDir(), "child.pid")
			t.Setenv("WIREPROXY_TEST_CHILD", child)
			withFakeManagerBin(t, false)
			script := "#!/usr/bin/env bash\ntrap '' TERM\nsleep 300 &\necho $! > \"$WIREPROXY_TEST_CHILD\"\nwait\n"
			if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
				t.Fatalf("writing the stand-in manager: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelWhenThere(child, cancel)

			started := time.Now()
			_, err := run(ctx, action)

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
		})
	}
}

// SIGTERM comes first, so the script's own traps can clean up before anything is killed.
func TestRunAsksAScanToStopBeforeKillingIt(t *testing.T) {
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
	cancelWhenThere(ready, cancel)

	_, _ = run(ctx, "--check")

	if _, err := os.Stat(mark); err != nil {
		t.Errorf("the script was killed before its TERM trap could run: %v", err)
	}
}

// An install, a removal or the routing cleanup change the host, and a package manager killed halfway leaves its database
// interrupted. When the time is up the call returns at once and the action finishes in the background.
func TestRunLeavesAnActionThatChangesTheHostRunningWhenTheContextEnds(t *testing.T) {
	for _, action := range []string{actionInstall, actionRemove, actionFixRouting} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			ready, finished := filepath.Join(dir, "ready"), filepath.Join(dir, "finished")
			t.Setenv("WIREPROXY_TEST_READY", ready)
			t.Setenv("WIREPROXY_TEST_FINISHED", finished)
			withFakeManagerBin(t, false)
			script := "#!/usr/bin/env bash\necho ready > \"$WIREPROXY_TEST_READY\"\nsleep 1\necho finished > \"$WIREPROXY_TEST_FINISHED\"\n"
			if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
				t.Fatalf("writing the stand-in manager: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelWhenThere(ready, cancel)

			_, err := run(ctx, action)

			if err == nil || !strings.Contains(err.Error(), "background") {
				t.Errorf("run = %v, want an error that says the action goes on in the background", err)
			}
			if _, statErr := os.Stat(finished); statErr == nil {
				t.Error("run returned only after the script had finished")
			}
			for range 500 {
				if _, statErr := os.Stat(finished); statErr == nil {
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
			t.Error("the script never finished: it was stopped when its context ended")
		})
	}
}

// The panel owns the read end of a pipe, and when it exits the action's next write is a SIGPIPE that would cut a package
// manager in half: an action that changes the host gets files for its output.
func TestRunGivesAnActionThatChangesTheHostFilesNotPipes(t *testing.T) {
	dir := t.TempDir()
	fd1, fd2 := filepath.Join(dir, "fd1"), filepath.Join(dir, "fd2")
	t.Setenv("WIREPROXY_TEST_FD1", fd1)
	t.Setenv("WIREPROXY_TEST_FD2", fd2)
	withFakeManagerBin(t, false)
	script := "#!/usr/bin/env bash\nreadlink /proc/$$/fd/1 > \"$WIREPROXY_TEST_FD1\"\nreadlink /proc/$$/fd/2 > \"$WIREPROXY_TEST_FD2\"\n"
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}

	if _, err := run(context.Background(), actionInstall); err != nil {
		t.Fatalf("run: %v", err)
	}

	for _, path := range []string{fd1, fd2} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("the script did not report where its output goes: %v", err)
		}
		if target := strings.TrimSpace(string(raw)); target == "" || strings.HasPrefix(target, "pipe:") {
			t.Errorf("the action writes to %q, want a file", target)
		}
	}
}

// What an action that changes the host wrote comes back to the caller: its output when it succeeds, its standard error when
// it fails.
func TestRunReturnsWhatAnActionThatChangesTheHostWrote(t *testing.T) {
	withFakeManagerBin(t, false)
	script := "#!/usr/bin/env bash\necho out\necho oops >&2\n[ \"$1\" = --remove ] && exit 3\nexit 0\n"
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}

	out, err := run(context.Background(), actionInstall)
	if err != nil || strings.TrimSpace(string(out)) != "out" {
		t.Errorf("run(install) = %q, %v; want its output and no error", out, err)
	}
	_, err = run(context.Background(), actionRemove)
	if err == nil || !strings.Contains(err.Error(), "oops") || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("run(remove) error = %v; want its standard error and its exit status", err)
	}
}

// An action that outlives its budget ends with nobody waiting for it, and its two files have no name: the panel log is the
// only place left that can say why an install failed after the toast had already gone out.
func TestRunLogsHowAnActionThatOutlivedItsBudgetEnded(t *testing.T) {
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	t.Setenv("WIREPROXY_TEST_READY", ready)
	withFakeManagerBin(t, false)
	script := "#!/usr/bin/env bash\necho ready > \"$WIREPROXY_TEST_READY\"\nsleep 0.2\necho 'E: dpkg was interrupted' >&2\nexit 100\n"
	if err := os.WriteFile(managerBinPath, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stand-in manager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelWhenThere(ready, cancel)

	if _, err := run(ctx, actionInstall); err == nil {
		t.Fatal("run reported success although its context ended while the script was running")
	}

	for range 500 {
		for _, line := range logger.GetLogs(1000, "warning") {
			if strings.Contains(line, "E: dpkg was interrupted") && strings.Contains(line, "exit status 100") {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("nothing in the panel log says how the action ended after its time was up")
}
