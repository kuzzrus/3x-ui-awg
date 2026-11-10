package hostaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSystemdRun starts the command the way PID 1 does: detached, with an environment of its own.
const fakeSystemdRun = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "systemd $FAKE_SYSTEMD_VERSION ($FAKE_SYSTEMD_VERSION.1-fake)"; exit 0; fi
echo called >> "$FAKE_SYSTEMD_DIR/systemd-run.calls"
if [ -n "$FAKE_SYSTEMD_REFUSE" ]; then echo "$FAKE_SYSTEMD_REFUSE" >&2; exit 1; fi
printf '%s\n' "$@" > "$FAKE_SYSTEMD_DIR/systemd-run.args"
unit=
while [ $# -gt 0 ]; do
  case "$1" in
    --unit) unit=$2; shift 2 ;;
    --*) shift ;;
    *) break ;;
  esac
done
if [ -n "$FAKE_SYSTEMD_NO_START" ]; then exit 0; fi
setsid env -i PATH=/usr/local/bin:/usr/bin:/bin "$@" </dev/null >/dev/null 2>&1 &
echo $! > "$FAKE_SYSTEMD_DIR/$unit.pid"
exit 0
`

const fakeSystemctl = `#!/bin/bash
echo "$*" >> "$FAKE_SYSTEMD_DIR/systemctl.calls"
case "$1" in
  is-active)
    pid=$(cat "$FAKE_SYSTEMD_DIR/$3.pid" 2>/dev/null) || exit 4
    kill -0 "$pid" 2>/dev/null && exit 0
    exit 3 ;;
  stop)
    pid=$(cat "$FAKE_SYSTEMD_DIR/$2.pid" 2>/dev/null) || exit 5
    kill -TERM -- "-$pid" 2>/dev/null
    for i in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || exit 0; sleep 0.05; done
    kill -KILL -- "-$pid" 2>/dev/null
    exit 0 ;;
esac
exit 0
`

type fakeSystemd struct{ dir string }

// useFakeSystemd puts a stand-in systemd-run and systemctl first on PATH and makes Run start commands as units.
func useFakeSystemd(t *testing.T, version string) fakeSystemd {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"systemd-run": fakeSystemdRun, "systemctl": fakeSystemctl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SYSTEMD_DIR", dir)
	t.Setenv("FAKE_SYSTEMD_VERSION", version)
	t.Setenv("FAKE_SYSTEMD_REFUSE", "")
	t.Setenv("FAKE_SYSTEMD_NO_START", "")
	t.Setenv(modeEnv, "unit")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	resetSystemdProbes(t)
	return fakeSystemd{dir: dir}
}

func resetSystemdProbes(t *testing.T) {
	t.Helper()
	reset := func() {
		versionOnce = sync.Once{}
		versionValue = 0
		systemdBroken.Store(false)
	}
	reset()
	t.Cleanup(reset)
}

func (f fakeSystemd) read(name string) string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(raw)
}

func (f fakeSystemd) args() []string {
	return strings.Split(strings.TrimSpace(f.read("systemd-run.args")), "\n")
}

func (f fakeSystemd) unit() string {
	args := f.args()
	for i, arg := range args {
		if arg == "--unit" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func TestAUnitRunsTheScriptOutsideThePanelWithAnEnvironmentOfItsOwn(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	t.Setenv("XUI_TEST_PANEL_VAR", "panel")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:3128")

	res, err := Run(context.Background(), Spec{Name: "warpwp --install-current", Argv: bash(`printf '%s|%s' "${XUI_TEST_PANEL_VAR-unset}" "${HTTPS_PROXY-unset}"`)})

	if err != nil || string(res.Stdout) != "unset|http://proxy.example:3128" {
		t.Errorf("Run = %q, %v; want the proxy setting handed over and nothing else of the panel's environment", res.Stdout, err)
	}
	args := fake.args()
	if !contains(args, "--quiet") || !contains(args, "--collect") || !contains(args, "--property=RuntimeMaxSec=7200") {
		t.Errorf("systemd-run was called with %q, want --quiet, --collect and a run time cap of two hours", args)
	}
	if unit := fake.unit(); !strings.HasPrefix(unit, "x-ui-hostaction-warpwp-install-current-") {
		t.Errorf("the unit is called %q, want a name that says what runs", unit)
	}
	if n := len(args); n < 2 || args[n-2] != "/bin/bash" || !strings.HasSuffix(args[n-1], "/run.sh") {
		t.Errorf("the unit runs %q, want bash on the script of the run", args)
	}
}

func TestOldSystemdGetsNoCollectAndHasItsFailedUnitClearedByHand(t *testing.T) {
	fake := useFakeSystemd(t, "229")

	_, err := Run(context.Background(), Spec{Name: "fails", Argv: bash(`exit 7`)})

	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != 7 {
		t.Fatalf("Run error = %v, want exit status 7", err)
	}
	if contains(fake.args(), "--collect") || !contains(fake.args(), "--property=RuntimeMaxSec=7200") {
		t.Errorf("systemd-run was called with %q, want a run time cap but no --collect", fake.args())
	}
	if want := "reset-failed " + fake.unit(); !strings.Contains(fake.read("systemctl.calls"), want) {
		t.Errorf("systemctl was called with %q, want %q", fake.read("systemctl.calls"), want)
	}
}

func TestAnAncientSystemdGetsNoRunTimeCap(t *testing.T) {
	fake := useFakeSystemd(t, "219")

	if _, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`exit 0`)}); err != nil {
		t.Fatal(err)
	}

	for _, arg := range fake.args() {
		if strings.HasPrefix(arg, "--property=") || arg == "--collect" {
			t.Errorf("systemd-run was called with %q, which systemd 219 does not know", arg)
		}
	}
}

func TestWithoutAnyoneForcingItCommandsGoToATransientUnit(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	t.Setenv(modeEnv, "")

	if _, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`exit 0`)}); err != nil {
		t.Fatal(err)
	}

	if fake.unit() == "" {
		t.Error("systemd-run was never called, the command did not run as a unit")
	}
}

func TestWhereSystemdIsNotRunningCommandsFallBackToChildrenOfThePanelAndSystemdIsNotAskedAgain(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	t.Setenv(modeEnv, "")
	t.Setenv("FAKE_SYSTEMD_REFUSE", "System has not been booted with systemd as init system (PID 1). Can't operate.")

	for i := range 2 {
		res, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`echo hi`)})
		if err != nil || string(res.Stdout) != "hi\n" {
			t.Fatalf("run %d = %q, %v; want the command to run directly", i, res.Stdout, err)
		}
	}

	if calls := strings.Count(fake.read("systemd-run.calls"), "called"); calls != 1 {
		t.Errorf("systemd-run was called %d times, want once: it said systemd is not running", calls)
	}
}

func TestAnyOtherFailureOfSystemdRunIsAnErrorAndTheCommandDoesNotRunTwice(t *testing.T) {
	useFakeSystemd(t, "255")
	t.Setenv(modeEnv, "")
	t.Setenv("FAKE_SYSTEMD_REFUSE", "Failed to start transient service unit: Access denied")
	marker := filepath.Join(t.TempDir(), "ran")

	_, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`touch "$1"`, marker)})

	time.Sleep(200 * time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "Access denied") || exists(marker) {
		t.Errorf("Run = %v, command ran: %v; want the answer of systemd-run as the error and no direct run", err, exists(marker))
	}
}

func TestACommandForcedIntoAUnitDoesNotFallBackWhenSystemdIsNotRunning(t *testing.T) {
	useFakeSystemd(t, "255")
	t.Setenv("FAKE_SYSTEMD_REFUSE", "System has not been booted with systemd as init system (PID 1). Can't operate.")
	marker := filepath.Join(t.TempDir(), "ran")

	_, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`touch "$1"`, marker)})

	time.Sleep(200 * time.Millisecond)
	if err == nil || exists(marker) {
		t.Errorf("Run = %v, command ran: %v; want an error and no command", err, exists(marker))
	}
}

func TestAStoppedUnitIsStoppedThroughSystemctlWithItsOwnCapAndGrace(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(pidFile, cancel)

	_, err := Run(ctx, Spec{Name: "scan", StopOnCancel: true, MaxRuntime: 10 * time.Minute, Argv: bash(`echo $$ > "$1"; sleep 300`, pidFile)})

	if !errors.Is(err, context.Canceled) {
		t.Errorf("Run error = %v, want the context error", err)
	}
	if want := "stop " + fake.unit(); !strings.Contains(fake.read("systemctl.calls"), want) {
		t.Errorf("systemctl was called with %q, want %q", fake.read("systemctl.calls"), want)
	}
	if !contains(fake.args(), "--property=RuntimeMaxSec=600") || !contains(fake.args(), "--property=TimeoutStopSec=15") {
		t.Errorf("systemd-run was called with %q, want the cap of this command and a short stop timeout", fake.args())
	}
	if alive(pidIn(t, pidFile)) {
		t.Error("the command still runs after its unit was stopped")
	}
}

func TestAUnitThatEndedWithoutAStatusIsNoticedWithoutWaitingForTheContext(t *testing.T) {
	useFakeSystemd(t, "255")
	t.Setenv("FAKE_SYSTEMD_NO_START", "1")
	checkOftenWhetherUnitsRun(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	started := time.Now()
	_, err := Run(ctx, Spec{Name: "killed", Argv: bash(`sleep 300`)})

	var exit *ExitError
	if !errors.As(err, &exit) || exit.Code != -1 || time.Since(started) > 20*time.Second {
		t.Errorf("Run = %v after %v; want an ExitError without an exit status, long before the context ends", err, time.Since(started))
	}
}

func TestAUnitThatOutlivesTheContextGoesOnWithoutThePanelAndItsEndIsLogged(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	dir := t.TempDir()
	ready, release := filepath.Join(dir, "ready"), releaseFile(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(ready, cancel)
	name := unique("warpwp --install-current")

	_, err := Run(ctx, Spec{Name: name, Argv: bash(`echo ready > "$1"; while [ ! -e "$2" ]; do sleep 0.05; done; echo 'E: no network' >&2; exit 9`, ready, release)})

	var detached *DetachedError
	if !errors.As(err, &detached) || !strings.Contains(err.Error(), "as unit "+fake.unit()) || !strings.Contains(err.Error(), "goes on without the panel") {
		t.Fatalf("Run error = %v; want a DetachedError that names the unit and says it goes on without the panel", err)
	}
	if _, m := onlyRun(t); m.Name != name || m.Unit != fake.unit() || m.PID != 0 || m.Started == 0 {
		t.Errorf("the record of the running unit is %+v, want its name and its unit for a restarted panel to find it by", m)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the panel log to say how the unit ended", func() bool {
		return logged("warning", name, "exit status 9", "E: no network")
	})
}
