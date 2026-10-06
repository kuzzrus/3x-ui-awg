//go:build linux

package naiveproxy

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fakeChild is a running copy of this test binary, in its fake-Caddy mode, installed at path and
// started in dir (this process's own folder when empty).
type fakeChild struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startFakeChildAt(t *testing.T, path, dir string) *fakeChild {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o755); err != nil {
		t.Fatal(err)
	}
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(path)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NAIVE_FAKE_CHILD=1", "NAIVE_FAKE_PIDFILE="+filepath.Join(t.TempDir(), "pids"))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &fakeChild{cmd: cmd, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(child.done)
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-child.done
	})
	return child
}

func (c *fakeChild) exited(within time.Duration) bool {
	select {
	case <-c.done:
		return true
	case <-time.After(within):
		return false
	}
}

// Caddy is a common name: the sweep exists for the panel's own leftovers, and a Caddy the admin
// runs for a website, from another file, has to outlive the panel's start.
func TestKillStrayCaddyProcessesSparesAnotherCaddy(t *testing.T) {
	root := t.TempDir()
	ours := filepath.Join(root, "bin", "naiveproxy", "caddy")
	orphan := startFakeChildAt(t, ours, "")
	foreign := startFakeChildAt(t, filepath.Join(root, "usr", "bin", "caddy"), filepath.Join(root, "srv", "site"))

	if got := killStrayCaddyProcesses(ours); got != 1 {
		t.Fatalf("killStrayCaddyProcesses killed %d processes, want only the panel's own leftover", got)
	}

	if !orphan.exited(3 * time.Second) {
		t.Error("the panel's own leftover Caddy survived the sweep")
	}
	if foreign.exited(300 * time.Millisecond) {
		t.Error("the sweep killed a Caddy that is not the panel's")
	}
	if err := syscall.Kill(foreign.cmd.Process.Pid, 0); err != nil {
		t.Errorf("the other Caddy is gone: %v", err)
	}
}

// A reinstall replaces the binary under a running sidecar, which /proc then reports as deleted.
func TestKillStrayCaddyProcessesFindsALeftoverWhoseBinaryWasRemoved(t *testing.T) {
	ours := filepath.Join(t.TempDir(), "bin", "naiveproxy", "caddy")
	orphan := startFakeChildAt(t, ours, "")
	if err := os.Remove(ours); err != nil {
		t.Fatal(err)
	}

	if got := killStrayCaddyProcesses(ours); got != 1 {
		t.Fatalf("killStrayCaddyProcesses killed %d processes, want the leftover that lost its binary", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover whose binary was removed survived the sweep")
	}
}

// The bin folder is often reached through a symlink, while /proc shows the real path.
func TestKillStrayCaddyProcessesResolvesASymlinkedBinFolder(t *testing.T) {
	root := t.TempDir()
	orphan := startFakeChildAt(t, filepath.Join(root, "real", "naiveproxy", "caddy"), "")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}

	if got := killStrayCaddyProcesses(filepath.Join(root, "bin", "naiveproxy", "caddy")); got != 1 {
		t.Fatalf("killStrayCaddyProcesses killed %d processes through a symlink, want 1", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover reached through a symlinked bin folder survived the sweep")
	}
}

// install.sh moves bin/ aside, deletes the panel folder and puts a fresh one back, so a leftover
// then runs a binary under the removed backup folder, from a folder that was replaced.
func TestKillStrayCaddyProcessesFindsALeftoverAfterAReinstall(t *testing.T) {
	root := t.TempDir()
	panel := filepath.Join(root, "x-ui")
	ours := filepath.Join(panel, "bin", "naiveproxy", "caddy")
	orphan := startFakeChildAt(t, ours, panel)

	backup := filepath.Join(root, "x-ui-bin-backup")
	if err := os.Rename(filepath.Join(panel, "bin"), backup); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(panel); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(ours), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ours, []byte("a new inode, as cp -a leaves it"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(backup); err != nil {
		t.Fatal(err)
	}
	t.Chdir(panel)

	if got := killStrayCaddyProcesses(ours); got != 1 {
		t.Fatalf("killStrayCaddyProcesses killed %d processes after a reinstall, want the panel's leftover", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover from before a reinstall survived the sweep")
	}
}
