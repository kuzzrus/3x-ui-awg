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

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction/hostactiontest"
)

// useFakeSystemd puts a stand-in systemd-run and systemctl first on PATH and makes Run start commands as units.
func useFakeSystemd(t *testing.T, version string) hostactiontest.Fake {
	t.Helper()
	fake := hostactiontest.Use(t, version)
	resetSystemdProbes(t)
	return fake
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
	args := fake.Args()
	if !contains(args, "--quiet") || !contains(args, "--collect") || !contains(args, "--property=RuntimeMaxSec=7200") {
		t.Errorf("systemd-run was called with %q, want --quiet, --collect and a run time cap of two hours", args)
	}
	if unit := fake.Unit(); !strings.HasPrefix(unit, "x-ui-hostaction-warpwp-install-current-") {
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
	if contains(fake.Args(), "--collect") || !contains(fake.Args(), "--property=RuntimeMaxSec=7200") {
		t.Errorf("systemd-run was called with %q, want a run time cap but no --collect", fake.Args())
	}
	if want := "reset-failed " + fake.Unit(); !strings.Contains(fake.Read("systemctl.calls"), want) {
		t.Errorf("systemctl was called with %q, want %q", fake.Read("systemctl.calls"), want)
	}
}

func TestAnAncientSystemdGetsNoRunTimeCap(t *testing.T) {
	fake := useFakeSystemd(t, "219")

	if _, err := Run(context.Background(), Spec{Name: "ok", Argv: bash(`exit 0`)}); err != nil {
		t.Fatal(err)
	}

	for _, arg := range fake.Args() {
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

	if fake.Unit() == "" {
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

	if calls := strings.Count(fake.Read("systemd-run.calls"), "called"); calls != 1 {
		t.Errorf("systemd-run was called %d times, want once: it said systemd is not running", calls)
	}
}

// A bus that is away for a moment (systemd re-executing itself) must not turn units off until the panel restarts.
func TestABusFailureOnlyCostsTheActionThatMetItAndSystemdIsAskedAgainNext(t *testing.T) {
	fake := useFakeSystemd(t, "255")
	t.Setenv(modeEnv, "")
	t.Setenv("FAKE_SYSTEMD_REFUSE", "Failed to connect to bus: No such file or directory")
	if res, err := Run(context.Background(), Spec{Name: "during the outage", Argv: bash(`echo direct`)}); err != nil || string(res.Stdout) != "direct\n" {
		t.Fatalf("the run during the outage = %q, %v; want the command to run directly", res.Stdout, err)
	}
	t.Setenv("FAKE_SYSTEMD_REFUSE", "")

	res, err := Run(context.Background(), Spec{Name: "after it", Argv: bash(`echo unit`)})

	if err != nil || string(res.Stdout) != "unit\n" || !strings.HasPrefix(fake.Unit(), "x-ui-hostaction-after-it-") {
		t.Errorf("the run after the outage = %q, %v in unit %q; want it to run as a unit again", res.Stdout, err, fake.Unit())
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
	if want := "stop " + fake.Unit(); !strings.Contains(fake.Read("systemctl.calls"), want) {
		t.Errorf("systemctl was called with %q, want %q", fake.Read("systemctl.calls"), want)
	}
	if !contains(fake.Args(), "--property=RuntimeMaxSec=600") || !contains(fake.Args(), "--property=TimeoutStopSec=15") {
		t.Errorf("systemd-run was called with %q, want the cap of this command and a short stop timeout", fake.Args())
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
	ready, release := filepath.Join(dir, "ready"), filepath.Join(dir, "release")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelOnceThere(ready, cancel)
	name := unique("warpwp --install-current")

	_, err := Run(ctx, Spec{Name: name, Argv: bash(waitForRelease+`echo 'E: no network' >&2; exit 9`, ready, release)})

	var detached *DetachedError
	if !errors.As(err, &detached) || !strings.Contains(err.Error(), "as unit "+fake.Unit()) || !strings.Contains(err.Error(), "goes on without the panel") {
		t.Fatalf("Run error = %v; want a DetachedError that names the unit and says it goes on without the panel", err)
	}
	if _, m := onlyRun(t); m.Name != name || m.Unit != fake.Unit() || m.PID != 0 || m.Started == 0 {
		t.Errorf("the record of the running unit is %+v, want its name and its unit for a restarted panel to find it by", m)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "the panel log to say how the unit ended", func() bool {
		return logged("warning", name, "exit status 9", "E: no network")
	})
}
