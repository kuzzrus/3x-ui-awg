//go:build linux

package naiveproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// killStrayCaddyProcesses ends the panel's own leftover sidecars from a previous run. "caddy" is a
// common name, so only a process running binaryPath itself counts: a user's own Caddy must survive.
func killStrayCaddyProcesses(binaryPath string) int {
	want := resolvedPath(binaryPath)
	if want == "" {
		return 0
	}
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
		if procExe(pid) != want {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}
	return killed
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

// procExe is the file pid runs. A binary replaced since the process started reads as "<path> (deleted)".
func procExe(pid int) string {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(exe, " (deleted)")
}
