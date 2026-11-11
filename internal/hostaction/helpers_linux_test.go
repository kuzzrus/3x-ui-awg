package hostaction

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// useDirect makes Run start commands as children of the test, with the folder of the runs in a temp dir.
func useDirect(t *testing.T) {
	t.Helper()
	t.Setenv(modeEnv, "direct")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
}

// bash is the command line for a script that gets args as $1, $2, and so on.
func bash(script string, args ...string) []string {
	return append([]string{"/bin/bash", "-c", script, "test"}, args...)
}

var nameSeq atomic.Uint64

// unique makes a command name no other run has used: the panel log outlives a test, and a count would span the runs of -count.
func unique(name string) string {
	return fmt.Sprintf("%s #%d-%d", name, time.Now().UnixNano(), nameSeq.Add(1))
}

// waitForRelease starts a script that says it is ready and waits for the file named by its second argument. It also ends
// once the folder of that file is gone, so a test that fails halfway leaves no script spinning when its folder is removed.
const waitForRelease = `echo ready > "$1"; while [ ! -e "$2" ] && [ -d "${2%/*}" ]; do sleep 0.05; done; `

func checkOftenWhetherUnitsRun(t *testing.T) {
	t.Helper()
	real := aliveEvery.Load()
	aliveEvery.Store(int64(100 * time.Millisecond))
	t.Cleanup(func() { aliveEvery.Store(real) })
}

func waitFor(t *testing.T, limit time.Duration, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(limit); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out after %v waiting for %s", limit, what)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// logged reports whether one line of the panel log, at the given level or worse, holds every part.
func logged(level string, parts ...string) bool {
	for _, line := range logger.GetLogs(1000, level) {
		all := true
		for _, part := range parts {
			all = all && strings.Contains(line, part)
		}
		if all {
			return true
		}
	}
	return false
}

func countLogged(level, part string) int {
	n := 0
	for _, line := range logger.GetLogs(1000, level) {
		if strings.Contains(line, part) {
			n++
		}
	}
	return n
}

// alive reports whether the process exists and is not a zombie waiting to be reaped.
func alive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	state := strings.Fields(string(stat[strings.LastIndex(string(stat), ")")+1:]))
	return len(state) > 0 && state[0] != "Z" && state[0] != "X"
}

func pidIn(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatalf("the pid in %s: %v", path, err)
	}
	return pid
}

// onlyRun is the folder of the one command that runs now, and what it recorded for a restarted panel to find it by.
func onlyRun(t *testing.T) (string, meta) {
	t.Helper()
	base, err := baseDir()
	if err != nil {
		t.Fatal(err)
	}
	names := entries(t, base)
	if len(names) != 1 {
		t.Fatalf("the runs folder holds %v, want the folder of one command", names)
	}
	dir := filepath.Join(base, names[0])
	var m meta
	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil || json.Unmarshal(raw, &m) != nil {
		t.Fatalf("the record of the command in %s: %v", dir, err)
	}
	return dir, m
}

func entries(t *testing.T, dir string) []string {
	t.Helper()
	list, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("listing %s: %v", dir, err)
	}
	names := make([]string, len(list))
	for i, entry := range list {
		names[i] = entry.Name()
	}
	return names
}
