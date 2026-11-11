package hostaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunReturnsWhatTheCommandWroteAndHowItEnded(t *testing.T) {
	useDirect(t)

	res, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`echo out; echo err >&2`)})
	if err != nil || string(res.Stdout) != "out\n" || string(res.Stderr) != "err\n" {
		t.Errorf("Run = %q, %q, %v; want its output, its error output and no error", res.Stdout, res.Stderr, err)
	}

	res, err = Run(context.Background(), Spec{Name: "fails", Argv: bash(`echo out; echo oops >&2; exit 3`)})
	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 3 || exit.Stderr != "oops" || string(res.Stdout) != "out\n" {
		t.Errorf("Run = %q, %v; want an ExitError with code 3 and 'oops', and the output it wrote", res.Stdout, err)
	}
	if !strings.Contains(err.Error(), "exit status 3: oops") {
		t.Errorf("the error reads %q, want it to say the exit status and the error output", err)
	}
}

func TestRunHandsEveryArgumentOverUnchanged(t *testing.T) {
	useDirect(t)
	args := []string{"plain", "two words", "it's", `"double"`, "$HOME", "${HOME}", "$$", "`id`", "line\nbreak", "-n", "", `back\slash`, "*", "%s", ";", "&&"}

	res, err := Run(context.Background(), Spec{Name: "args", Argv: append([]string{"printf", "%s|"}, args...)})

	if want := strings.Join(args, "|") + "|"; err != nil || string(res.Stdout) != want {
		t.Errorf("the command saw %q, %v; want %q", res.Stdout, err, want)
	}
}

func TestRunFeedsStdinAndEnvironment(t *testing.T) {
	useDirect(t)

	res, err := Run(context.Background(), Spec{
		Name:  "input",
		Argv:  bash(`read -r a; read -r b; echo "$a-$b-$DEBIAN_FRONTEND"`),
		Stdin: "x\ny\n",
		Env:   []string{"DEBIAN_FRONTEND=noninteractive"},
	})

	if err != nil || string(res.Stdout) != "x-y-noninteractive\n" {
		t.Errorf("Run = %q, %v; want the two lines of input and the setting", res.Stdout, err)
	}
}

func TestASettingTheCallerNamesWinsOverTheProxySettingOfThePanel(t *testing.T) {
	useDirect(t)
	t.Setenv("https_proxy", "http://panel.example:3128")
	t.Setenv("http_proxy", "http://panel.example:3128")

	res, err := Run(context.Background(), Spec{
		Name: "proxied",
		Argv: bash(`printf '%s|%s' "$https_proxy" "$http_proxy"`),
		Env:  []string{"https_proxy=http://caller.example:8080"},
	})

	if want := "http://caller.example:8080|http://panel.example:3128"; err != nil || string(res.Stdout) != want {
		t.Errorf("the command saw %q, %v; want the caller's https_proxy and the panel's http_proxy", res.Stdout, err)
	}
}

func TestRunKeepsItsFilesToItselfAndGivesTheCommandFilesNotPipes(t *testing.T) {
	useDirect(t)

	res, err := Run(context.Background(), Spec{Name: "where", Argv: bash(`stat -c %a "$HOSTACTION_DIR"; readlink /proc/$$/fd/1 /proc/$$/fd/2`)})

	lines := strings.Fields(string(res.Stdout))
	if err != nil || len(lines) != 3 || lines[0] != "700" || !strings.HasSuffix(lines[1], "/stdout") || !strings.HasSuffix(lines[2], "/stderr") {
		t.Errorf("the command saw %q, %v; want the folder private to its owner and its output going to files", res.Stdout, err)
	}
}

func TestConcurrentRunsDoNotShareFiles(t *testing.T) {
	useDirect(t)
	results := make([]string, 20)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := Run(context.Background(), Spec{Name: "echo", Argv: bash(`sleep 0.05; echo "$1"`, strconv.Itoa(i))})
			if err != nil {
				t.Errorf("run %d: %v", i, err)
			}
			results[i] = strings.TrimSpace(string(res.Stdout))
		}()
	}
	wg.Wait()

	for i, got := range results {
		if got != strconv.Itoa(i) {
			t.Errorf("run %d got the output %q", i, got)
		}
	}
}

func TestAFailedCommandReportsTheEndOfItsErrorOutput(t *testing.T) {
	useDirect(t)

	_, err := Run(context.Background(), Spec{Name: "noisy", Argv: bash(`head -c 100000 /dev/zero | tr '\0' a >&2; echo THE-END >&2; exit 1`)})

	var exit *ExitError
	if !errors.As(err, &exit) || len(exit.Stderr) > errorTail || !strings.HasSuffix(exit.Stderr, "THE-END") {
		t.Errorf("Run error = %v; want the last %d bytes of the error output, ending in THE-END", err, errorTail)
	}
}

func TestACommandWhoseScriptIsKilledEndsWithoutAnExitStatusAtOnce(t *testing.T) {
	useDirect(t)

	started := time.Now()
	_, err := Run(context.Background(), Spec{Name: "killed", Argv: bash(`kill -9 $PPID`)})

	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != -1 || time.Since(started) > 2*time.Second {
		t.Errorf("Run = %v after %v; want an ExitError without an exit status as soon as the script is gone", err, time.Since(started))
	}
}

func TestRunWithNothingToRunIsAnErrorNotASilentSuccess(t *testing.T) {
	useDirect(t)

	if _, err := Run(context.Background(), Spec{Name: "nothing"}); err == nil {
		t.Fatal("Run with no command reported success")
	}
	base, _ := baseDir()
	if exists(base) && len(entries(t, base)) != 0 {
		t.Errorf("a refused spec left %v behind", entries(t, base))
	}
}

func TestRunDoesNotStartWhenItsContextIsAlreadyDone(t *testing.T) {
	useDirect(t)
	marker := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Run(ctx, Spec{Name: "late", Argv: bash(`touch "$1"`, marker)})

	time.Sleep(200 * time.Millisecond)
	if !errors.Is(err, context.Canceled) || exists(marker) {
		t.Errorf("Run = %v, command ran: %v; want the context error and no command", err, exists(marker))
	}
}

func TestACommandIsRefusedWhereTheSandboxMakesUsrReadOnlyAndNoUnitCanBeStarted(t *testing.T) {
	useDirect(t)
	real := readOnlyUsr
	readOnlyUsr = func() bool { return true }
	t.Cleanup(func() { readOnlyUsr = real })
	marker := filepath.Join(t.TempDir(), "ran")

	_, err := Run(context.Background(), Spec{Name: "apt-get install -y tor", Argv: bash(`touch "$1"`, marker)})

	time.Sleep(200 * time.Millisecond)
	base, _ := baseDir()
	if !errors.Is(err, ErrSandboxed) || exists(marker) || len(entries(t, base)) != 0 {
		t.Errorf("Run = %v, command ran: %v, left %v; want ErrSandboxed, no command and no files", err, exists(marker), entries(t, base))
	}
}

func withShortGrace(t *testing.T) {
	t.Helper()
	real := killGrace
	killGrace = 300 * time.Millisecond
	t.Cleanup(func() { killGrace = real })
}

// cancelOnceThere cancels the context when the file appears, so a test acts while its command demonstrably runs.
func cancelOnceThere(path string, cancel context.CancelFunc) {
	go func() {
		for range 500 {
			if exists(path) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
}

func TestAStoppedCommandTakesEverythingItStartedWithIt(t *testing.T) {
	useDirect(t)
	withShortGrace(t)
	child := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(child, cancel)

	started := time.Now()
	_, err := Run(ctx, Spec{Name: "scan", StopOnCancel: true, Argv: bash(`trap '' TERM; sleep 300 & echo $! > "$1"; wait`, child)})

	pid := pidIn(t, child)
	if !errors.Is(err, context.Canceled) || time.Since(started) > 10*time.Second || alive(pid) {
		t.Errorf("Run = %v after %v, child %d alive: %v; want the context error, a prompt return and the child gone", err, time.Since(started), pid, alive(pid))
	}
}

func TestAStoppedCommandIsAskedToLeaveBeforeItIsKilled(t *testing.T) {
	useDirect(t)
	withShortGrace(t)
	dir := t.TempDir()
	ready, mark := filepath.Join(dir, "ready"), filepath.Join(dir, "cleaned")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(ready, cancel)

	_, _ = Run(ctx, Spec{Name: "scan", StopOnCancel: true, Argv: bash(`trap 'echo cleaned > "$2"; exit 0' TERM; echo ready > "$1"; while :; do sleep 0.05; done`, ready, mark)})

	if !exists(mark) {
		t.Error("the command was killed before its TERM handler could run")
	}
}

func TestACommandThatOutlivesItsContextIsLeftToFinishAndItsEndIsLogged(t *testing.T) {
	useDirect(t)
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), releaseFile(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(ready, cancel)
	name := unique("apt-get install -y tor")

	_, err := Run(ctx, Spec{Name: name, Argv: bash(`echo ready > "$1"; while [ ! -e "$2" ]; do sleep 0.05; done; echo 'E: dpkg was interrupted' >&2; exit 100`, ready, release)})

	var detached *DetachedError
	if !errors.As(err, &detached) || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Run error = %v; want a DetachedError wrapping the context error that says it is still running", err)
	}
	base, _ := baseDir()
	if _, m := onlyRun(t); m.Name != name || m.PID <= 0 || !alive(m.PID) || m.Unit != "" || m.MaxRuntime != 7200 || m.Started == 0 {
		t.Errorf("the record of the running command is %+v, want its name, its process, its run time cap and when it started", m)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the panel log to say how the command ended", func() bool {
		return logged("warning", name, "exit status 100", "E: dpkg was interrupted")
	})
	waitFor(t, 5*time.Second, "the folder of the ended command to be dropped", func() bool { return len(entries(t, base)) == 0 })
}
