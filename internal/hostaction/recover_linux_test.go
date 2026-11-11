package hostaction

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// leftBehind makes the folder an earlier panel process would have left for a command.
func leftBehind(t *testing.T, name string, m meta, files map[string]string) string {
	t.Helper()
	base, err := baseDir()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	files["meta.json"] = string(raw)
	for file, content := range files {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRecoverLogsHowACommandThatEndedWhilePanelWasAwayEndedAndDropsItsFolder(t *testing.T) {
	useDirect(t)
	name := unique("warpwp --install-current")
	dir := leftBehind(t, "old-run", meta{Name: name, Started: time.Now().Unix(), MaxRuntime: 7200},
		map[string]string{"status": "100\n", "stderr": "E: dpkg was interrupted\n"})

	Recover()

	if !logged("warning", name, "exit status 100", "E: dpkg was interrupted") || exists(dir) {
		t.Errorf("logged: %v, folder still there: %v; want the outcome in the panel log and the folder gone",
			logged("warning", name, "exit status 100"), exists(dir))
	}
}

func TestRecoverWatchesACommandThatStillRunsAndDropsItOnceItHasEnded(t *testing.T) {
	useDirect(t)
	sleeper := exec.CommandContext(context.Background(), "sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
	dir := leftBehind(t, "running", meta{Name: unique("tor install"), PID: sleeper.Process.Pid, Started: time.Now().Unix(), MaxRuntime: 7200}, map[string]string{})

	Recover()

	time.Sleep(300 * time.Millisecond)
	if !exists(dir) {
		t.Fatal("the folder of a command that still runs was dropped")
	}
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the folder of the command that has ended to be dropped", func() bool { return !exists(dir) })
}

func TestRecoverDropsACommandThatDiedWithoutLeavingAStatus(t *testing.T) {
	useDirect(t)
	gone := exec.CommandContext(context.Background(), "true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	name := unique("tor install")
	dir := leftBehind(t, "died", meta{Name: name, PID: gone.Process.Pid, Started: time.Now().Unix(), MaxRuntime: 7200}, map[string]string{})

	Recover()

	if !logged("warning", name, "ended without leaving an exit status") || exists(dir) {
		t.Errorf("logged: %v, folder still there: %v; want a warning and the folder gone",
			logged("warning", name, "ended without leaving an exit status"), exists(dir))
	}
}

func TestRecoverDropsAFolderWithoutARecordOnlyOnceItIsOld(t *testing.T) {
	useDirect(t)
	base, _ := baseDir()
	fresh, old := filepath.Join(base, "fresh"), filepath.Join(base, "old")
	for _, dir := range []string{fresh, old} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * time.Minute)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatal(err)
	}

	Recover()

	if !exists(fresh) || exists(old) {
		t.Errorf("fresh kept: %v, old kept: %v; want the folder of a run that is just starting kept and the old one dropped", exists(fresh), exists(old))
	}
}

func TestRecoverLeavesTheCommandsOfThisProcessAlone(t *testing.T) {
	useDirect(t)
	checkOftenWhetherUnitsRun(t)
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(ready, cancel)
	name := unique("own-command")
	_, _ = Run(ctx, Spec{Name: name, Argv: bash(waitForRelease+`echo oops >&2; exit 4`, ready, release)})
	base, _ := baseDir()

	Recover()

	if len(entries(t, base)) != 1 {
		t.Fatalf("Recover touched the folder of a command this process runs: %v", entries(t, base))
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the end of the command to be logged", func() bool { return logged("warning", name, "exit status 4") })
	waitFor(t, 5*time.Second, "the folder of the ended command to be dropped", func() bool { return len(entries(t, base)) == 0 })
	time.Sleep(1500 * time.Millisecond)
	if n := countLogged("warning", name); n != 1 {
		t.Errorf("the end of the command was logged %d times, want once: a second watcher was started for it", n)
	}
}
