//go:build linux

package wireproxy

import (
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
		group := -cmd.Process.Pid
		go func() {
			select {
			case <-exited:
			case <-time.After(grace):
				_ = syscall.Kill(group, syscall.SIGKILL)
			}
		}()
		return syscall.Kill(group, syscall.SIGTERM)
	}
	cmd.WaitDelay = grace + 5*time.Second
	return func() { close(exited) }
}
