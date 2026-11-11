//go:build linux

package hostaction

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	systemdRunTimeout = 15 * time.Second
	systemctlTimeout  = 30 * time.Second
	stRdonly          = 1
)

// killGrace is how long a stopped command and what it started get after SIGTERM before SIGKILL; a var for tests.
var killGrace = 10 * time.Second

var (
	systemdBroken atomic.Bool
	versionOnce   sync.Once
	versionValue  int
)

// readOnlyUsr is true under the panel's own sandbox (ProtectSystem), where nothing below /usr or /etc can be written.
var readOnlyUsr = func() bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/usr", &st); err != nil {
		return false
	}
	return uint64(st.Flags)&stRdonly != 0
}

func systemdUsable() bool {
	if systemdBroken.Load() {
		return false
	}
	_, err := exec.LookPath("systemd-run")
	return err == nil
}

func markSystemdUnusable() { systemdBroken.Store(true) }

// systemdVersion is the major version systemd-run reports, or 0 when it cannot be told.
func systemdVersion(bin string) int {
	versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, "--version").Output()
		if err != nil {
			return
		}
		if fields := strings.Fields(string(out)); len(fields) >= 2 {
			versionValue, _ = strconv.Atoi(fields[1])
		}
	})
	return versionValue
}

// directLauncher runs the script as a child of this process, in a session of its own.
type directLauncher struct{}

func (directLauncher) start(ctx context.Context, r *run) error {
	cmd := exec.CommandContext(context.WithoutCancel(ctx), "/bin/bash", r.path("run.sh"))
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	r.pid = cmd.Process.Pid
	r.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(r.exited)
	}()
	return nil
}

func (directLauncher) alive(r *run) bool {
	if r.exited == nil {
		return r.pid > 0 && syscall.Kill(r.pid, 0) == nil
	}
	select {
	case <-r.exited:
		return false
	default:
		return true
	}
}

// stop ends the whole process group: the script leaves before what it started, which may ignore SIGTERM.
func (directLauncher) stop(r *run) {
	group := -r.pid
	_ = syscall.Kill(group, syscall.SIGTERM)
	if groupGone(group, killGrace) {
		return
	}
	_ = syscall.Kill(group, syscall.SIGKILL)
	groupGone(group, 5*time.Second)
}

func (directLauncher) release(*run) {}

func groupGone(group int, limit time.Duration) bool {
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if errors.Is(syscall.Kill(group, 0), syscall.ESRCH) {
			return true
		}
	}
	return false
}

// unitLauncher runs the script as a transient systemd unit, outside this process's sandbox and control group.
type unitLauncher struct{}

func (unitLauncher) start(ctx context.Context, r *run) error {
	bin, err := exec.LookPath("systemd-run")
	if err != nil {
		return fmt.Errorf("%w: %w", errSystemdUnusable, err)
	}
	version := systemdVersion(bin)
	args := []string{"--quiet", "--unit", r.unit, "--description=3x-ui: " + r.spec.Name}
	if version >= 236 {
		args = append(args, "--collect")
		r.collected = true
	}
	if version >= 229 {
		args = append(args, "--property=RuntimeMaxSec="+strconv.Itoa(int(r.spec.maxRuntime().Seconds())))
	}
	if r.spec.StopOnCancel {
		args = append(args, "--property=TimeoutStopSec=15")
	}
	args = append(args, "/bin/bash", r.path("run.sh"))
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemdRunTimeout)
	defer cancel()
	out, err := exec.CommandContext(runCtx, bin, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	if SystemdUnavailable(text) {
		// An init that is not systemd stays so while the panel runs; a bus that is away may be back for the next command.
		if strings.Contains(text, notBootedMarker) {
			markSystemdUnusable()
		}
		return fmt.Errorf("%w: %s", errSystemdUnusable, text)
	}
	return fmt.Errorf("systemd-run: %w: %s", err, text)
}

// alive is false only when systemctl says the unit is inactive, failed or gone: a systemctl that cannot answer is no proof.
func (unitLauncher) alive(r *run) bool {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	var exit *exec.ExitError
	if err := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", r.unit).Run(); errors.As(err, &exit) {
		return exit.ExitCode() != 3 && exit.ExitCode() != 4
	}
	return true
}

func (unitLauncher) stop(r *run) {
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "systemctl", "stop", r.unit).Run()
}

// release clears a failed unit that systemd would otherwise keep until reboot, on systemd too old for --collect.
func (unitLauncher) release(r *run) {
	if r.collected {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), systemctlTimeout)
	defer cancel()
	_ = exec.CommandContext(ctx, "systemctl", "reset-failed", r.unit).Run()
}
