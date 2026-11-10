//go:build linux

package wireproxy

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long a stopped script and its children get to exit after SIGTERM before SIGKILL; a var for tests.
var killGrace = 10 * time.Second

// stopGroupOnCancel makes cmd a process group of its own and, when its context ends, stops the whole group: a scan starts
// probes that keep running, with their locks held, if only bash is killed. Call the returned func once cmd is waited for.
func stopGroupOnCancel(cmd *exec.Cmd) (finished func()) {
	exited := make(chan struct{})
	grace := killGrace
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A leader that is already reaped is not a failed cancel, and its pid can name another group by then.
		if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
			return err
		}
		group := -cmd.Process.Pid
		go func() {
			select {
			case <-exited:
			case <-time.After(grace):
				_ = syscall.Kill(group, syscall.SIGKILL)
			}
		}()
		err := syscall.Kill(group, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = grace + 5*time.Second
	return func() { close(exited) }
}
