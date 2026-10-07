//go:build linux

package tuic

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// killStrayTuicProcesses ends the panel's own leftover sidecars from a previous run. "tuic-server" is a name
// standalone servers use too: only the panel's own binary or folder, running one of its configs, counts.
func killStrayTuicProcesses(binaryPath string) int {
	want := resolvedPath(binaryPath)
	if want == "" {
		return 0
	}
	cwd := resolvedPath(".")
	configDir := filepath.Clean(ConfigDir())
	self := os.Getpid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	killed := 0
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if !isOwnSidecar(pid, want, cwd) || !isManagedTuicCmdline(pid, configDir) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGTERM); err == nil {
			killed++
			time.Sleep(50 * time.Millisecond)
			if err := syscall.Kill(pid, 0); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}
	return killed
}

// isOwnSidecar is true for a process running want, or a tuic-server that runs from the panel's folder: a
// reinstall renames bin/ aside and recreates the folder, which leaves a leftover with neither path.
func isOwnSidecar(pid int, want, cwd string) bool {
	exe := procLink(pid, "exe")
	if exe == want {
		return true
	}
	return exe != "" && cwd != "" && filepath.Base(exe) == filepath.Base(want) && procLink(pid, "cwd") == cwd
}

func isManagedTuicCmdline(pid int, configDir string) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return false
	}
	args := strings.Split(string(data), "\x00")
	for i, arg := range args {
		if arg == "-c" && i+1 < len(args) {
			cfg := filepath.Clean(args[i+1])
			if strings.HasPrefix(cfg, configDir) {
				return true
			}
		}
	}
	return false
}

// resolvedPath is path the way /proc/<pid>/exe shows it: absolute, symlinks resolved.
func resolvedPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// The binary itself may be gone (a reinstall removed it under a running process); its directory still resolves.
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		return filepath.Join(dir, filepath.Base(abs))
	}
	return abs
}

// procLink is where /proc/<pid>/<name> points; a removed target reads as "<path> (deleted)".
func procLink(pid int, name string) string {
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/%s", pid, name))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(target, " (deleted)")
}
