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
// common name, so only the panel's own binary or folder counts: a user's own Caddy must survive.
func killStrayCaddyProcesses(binaryPath string) int {
	want := resolvedPath(binaryPath)
	if want == "" {
		return 0
	}
	cwd := resolvedPath(".")
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
		if !isOwnSidecar(pid, want, cwd) {
			continue
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err == nil {
			killed++
		}
	}
	return killed
}

// isOwnSidecar is true for a process running want, or a caddy that runs from the panel's folder: a
// reinstall renames bin/ aside and recreates the folder, which leaves a leftover with neither path.
func isOwnSidecar(pid int, want, cwd string) bool {
	exe := procLink(pid, "exe")
	if exe == want {
		return true
	}
	return exe != "" && cwd != "" && filepath.Base(exe) == filepath.Base(want) && procLink(pid, "cwd") == cwd
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
