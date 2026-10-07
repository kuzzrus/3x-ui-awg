//go:build linux

package tuic

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestMain re-execs the test binary as a stand-in tuic-server (TUIC_FAKE_CHILD=1) that only waits to be killed.
func TestMain(m *testing.M) {
	if os.Getenv("TUIC_FAKE_CHILD") == "1" {
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

// fakeSidecar is a running copy of this test binary posing as the tuic-server installed at path.
type fakeSidecar struct {
	cmd  *exec.Cmd
	done chan struct{}
}

// startFakeSidecar runs it the way the panel runs tuic-server: from dir, with config as its -c file.
// A path that already holds a copy is reused, since a running binary cannot be rewritten.
func startFakeSidecar(t *testing.T, path, dir, config string) *fakeSidecar {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
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
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-c", config)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "TUIC_FAKE_CHILD=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	child := &fakeSidecar{cmd: cmd, done: make(chan struct{})}
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

func (c *fakeSidecar) exited(within time.Duration) bool {
	select {
	case <-c.done:
		return true
	case <-time.After(within):
		return false
	}
}

func requireAlive(t *testing.T, c *fakeSidecar, what string) {
	t.Helper()
	if c.exited(300 * time.Millisecond) {
		t.Errorf("the sweep killed %s", what)
		return
	}
	if err := syscall.Kill(c.cmd.Process.Pid, 0); err != nil {
		t.Errorf("%s is gone: %v", what, err)
	}
}

// A relative bin/tuic/tuic_1.json is the same string in every panel's folder, so another panel's
// sidecar has to outlive this one's start.
func TestKillStrayTuicProcessesSparesAnotherPanelsSidecar(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", "bin")
	root := t.TempDir()
	panel := filepath.Join(root, "x-ui")
	otherPanel := filepath.Join(root, "other-x-ui")
	orphan := startFakeSidecar(t, filepath.Join(panel, "bin", "tuic-server"), panel, ConfigPathForID(1))
	other := startFakeSidecar(t, filepath.Join(otherPanel, "bin", "tuic-server"), otherPanel, ConfigPathForID(1))
	t.Chdir(panel)

	if got := killStrayTuicProcesses("bin/tuic-server"); got != 1 {
		t.Fatalf("killStrayTuicProcesses killed %d processes, want only the panel's own leftover", got)
	}

	if !orphan.exited(3 * time.Second) {
		t.Error("the panel's own leftover survived the sweep")
	}
	requireAlive(t, other, "another panel's tuic-server")
}

// The -c file is what tells the panel's sidecar from a standalone server that shares its binary or folder.
func TestKillStrayTuicProcessesSparesAServerRunningItsOwnConfig(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", "bin")
	root := t.TempDir()
	panel := filepath.Join(root, "x-ui")
	ours := filepath.Join(panel, "bin", "tuic-server")
	orphan := startFakeSidecar(t, ours, panel, ConfigPathForID(1))
	sharedBinary := startFakeSidecar(t, ours, filepath.Join(root, "srv"), filepath.Join(root, "etc", "tuic.json"))
	sharedFolder := startFakeSidecar(t, filepath.Join(root, "usr", "bin", "tuic-server"), panel, filepath.Join(root, "etc", "tuic.json"))
	t.Chdir(panel)

	if got := killStrayTuicProcesses("bin/tuic-server"); got != 1 {
		t.Fatalf("killStrayTuicProcesses killed %d processes, want only the panel's own leftover", got)
	}

	if !orphan.exited(3 * time.Second) {
		t.Error("the panel's own leftover survived the sweep")
	}
	requireAlive(t, sharedBinary, "a standalone server running the panel's binary")
	requireAlive(t, sharedFolder, "a standalone server running from the panel's folder")
}

// A reinstall replaces the binary under a running sidecar, which /proc then reports as deleted.
// It runs from another folder, so only its binary path can find it.
func TestKillStrayTuicProcessesFindsALeftoverWhoseBinaryWasRemoved(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", filepath.Join(root, "bin"))
	ours := filepath.Join(root, "bin", "tuic-server")
	orphan := startFakeSidecar(t, ours, filepath.Join(root, "elsewhere"), ConfigPathForID(1))
	if err := os.Remove(ours); err != nil {
		t.Fatal(err)
	}

	if got := killStrayTuicProcesses(ours); got != 1 {
		t.Fatalf("killStrayTuicProcesses killed %d processes, want the leftover that lost its binary", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover whose binary was removed survived the sweep")
	}
}

// The bin folder is often reached through a symlink, while /proc shows the real path. The leftover
// runs from another folder, so only the resolved binary path can find it.
func TestKillStrayTuicProcessesResolvesASymlinkedBinFolder(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", filepath.Join(root, "bin"))
	orphan := startFakeSidecar(t, filepath.Join(root, "real", "tuic-server"), filepath.Join(root, "elsewhere"), ConfigPathForID(1))
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "bin")); err != nil {
		t.Fatal(err)
	}

	if got := killStrayTuicProcesses(filepath.Join(root, "bin", "tuic-server")); got != 1 {
		t.Fatalf("killStrayTuicProcesses killed %d processes through a symlink, want 1", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover reached through a symlinked bin folder survived the sweep")
	}
}

// install.sh moves bin/ aside, deletes the panel folder and puts a fresh one back, so a leftover
// then runs a binary under the removed backup folder, from a folder that was replaced.
func TestKillStrayTuicProcessesFindsALeftoverAfterAReinstall(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", "bin")
	root := t.TempDir()
	panel := filepath.Join(root, "x-ui")
	ours := filepath.Join(panel, "bin", "tuic-server")
	orphan := startFakeSidecar(t, ours, panel, ConfigPathForID(1))

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

	if got := killStrayTuicProcesses("bin/tuic-server"); got != 1 {
		t.Fatalf("killStrayTuicProcesses killed %d processes after a reinstall, want the panel's leftover", got)
	}
	if !orphan.exited(3 * time.Second) {
		t.Error("a leftover from before a reinstall survived the sweep")
	}
}
