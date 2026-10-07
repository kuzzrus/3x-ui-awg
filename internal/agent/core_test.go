//go:build !windows

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type fixture struct {
	t      *testing.T
	core   *Core
	state  *State
	binDir string
	logDir string
	port   int
}

// newFixture puts the fake core where the agent looks for the real one.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", binDir)
	if err := os.Symlink(self, filepath.Join(binDir, xray.GetBinaryName())); err != nil {
		t.Fatal(err)
	}
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, state: state, binDir: binDir, logDir: t.TempDir(), port: freePort(t)}
	f.core = NewCore(state, f.logDir)
	t.Cleanup(f.core.Close)
	return f
}

func (f *fixture) config(mark string) testConfig {
	return testConfig{apiPort: f.port, mark: mark}
}

func (f *fixture) apply(body []byte) (agentproto.ConfigResponse, error) {
	return f.core.Apply(context.Background(), body, false)
}

func (f *fixture) mustApply(body []byte) agentproto.ConfigResponse {
	f.t.Helper()
	resp, err := f.apply(body)
	if err != nil {
		f.t.Fatalf("Apply: %v", err)
	}
	return resp
}

// starts is how many times the fake core ran, which is how the tests tell a restart
// from a change taken in place.
func (f *fixture) starts() int {
	raw, err := os.ReadFile(filepath.Join(f.binDir, "fake.starts"))
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Count(string(raw), "start\n")
}

func (f *fixture) pid() int {
	f.t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.binDir, "fake.pid"))
	if err != nil {
		f.t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(raw))
	if err != nil {
		f.t.Fatal(err)
	}
	return pid
}

func (f *fixture) wantRevision(body []byte, restart bool) {
	f.t.Helper()
	want := agentproto.RevisionOf(body, restart)
	if got := f.core.Snapshot().Revision; got != want {
		f.t.Fatalf("running revision = %q, want %q", got, want)
	}
}

func (f *fixture) wantSavedBody(body []byte) {
	f.t.Helper()
	saved, err := f.state.LastGood()
	if err != nil || saved == nil || string(saved.Body) != string(body) {
		f.t.Fatalf("last good = %v, %v; want the config that last ran", saved, err)
	}
}

func wantConfigError(t *testing.T, err error, contains string) {
	t.Helper()
	var refused *ConfigError
	if !errors.As(err, &refused) {
		t.Fatalf("error = %v, want a *ConfigError", err)
	}
	if !strings.Contains(refused.Reason, contains) {
		t.Fatalf("reason = %q, want it to contain %q", refused.Reason, contains)
	}
}

func TestApplyStartsTheCoreAndRepeatsAreNoops(t *testing.T) {
	f := newFixture(t)
	body := f.config("a").compact(t)

	first := f.mustApply(body)
	want := agentproto.ConfigResponse{
		Revision:  agentproto.RevisionOf(body, false),
		Applied:   agentproto.AppliedRestart,
		XrayState: agentproto.XrayStateRunning,
	}
	if first != want {
		t.Fatalf("first apply = %+v, want %+v", first, want)
	}
	f.wantSavedBody(body)

	again := f.mustApply(body)
	want.Applied = agentproto.AppliedNoop
	if again != want {
		t.Fatalf("repeated apply = %+v, want %+v", again, want)
	}
	if got := f.starts(); got != 1 {
		t.Fatalf("core started %d times, want 1", got)
	}
	if snap := f.core.Snapshot(); snap.XrayVersion != "9.9.9" || snap.XrayError != "" {
		t.Fatalf("snapshot = %+v, want version 9.9.9 and no error", snap)
	}
}

// The bytes differ, so the revision does, but the core runs the same config.
func TestApplyOfAChangeThatLeavesTheConfigAloneIsANoop(t *testing.T) {
	f := newFixture(t)
	compact := f.config("a").compact(t)
	f.mustApply(compact)
	withNewline := append(append([]byte{}, compact...), '\n')

	resp := f.mustApply(withNewline)
	if resp.Applied != agentproto.AppliedNoop || resp.Revision != agentproto.RevisionOf(withNewline, false) {
		t.Fatalf("apply = %+v, want a noop under the new revision", resp)
	}
	if got := f.starts(); got != 1 {
		t.Fatalf("core started %d times, want 1", got)
	}
	f.wantRevision(withNewline, false)
	f.wantSavedBody(withNewline)

	if resp := f.mustApply(withNewline); resp.Applied != agentproto.AppliedNoop {
		t.Fatalf("repeating it = %+v, want noop", resp)
	}
}

// Only the restart policy differs, which the diff does not see but the revision does.
func TestApplyOfANewRestartPolicyIsRemembered(t *testing.T) {
	f := newFixture(t)
	body := f.config("a").compact(t)
	f.mustApply(body)

	resp, err := f.core.Apply(context.Background(), body, true)
	if err != nil || resp.Applied != agentproto.AppliedNoop {
		t.Fatalf("apply = %+v, %v; want noop", resp, err)
	}
	f.wantRevision(body, true)
	if saved, err := f.state.LastGood(); err != nil || saved == nil || !saved.RestartOnUserRemoval {
		t.Fatalf("last good = %v, %v; want the new policy stored", saved, err)
	}
}

func TestApplyRestartsForAChangeTheCoreCannotTakeInPlace(t *testing.T) {
	f := newFixture(t)
	f.mustApply(f.config("a").compact(t))
	pid := f.pid()

	body := f.config("b").compact(t)
	resp := f.mustApply(body)
	if resp.Applied != agentproto.AppliedRestart {
		t.Fatalf("applied = %q, want restart", resp.Applied)
	}
	if got := f.starts(); got != 2 || f.pid() == pid {
		t.Fatalf("core started %d times with pid %d, want a second process", got, f.pid())
	}
	f.wantRevision(body, false)
	f.wantSavedBody(body)
}

func TestApplyRefusesWhatTheCoreWouldNotRunAndKeepsTheOldConfig(t *testing.T) {
	f := newFixture(t)
	good := f.config("a").compact(t)
	f.mustApply(good)

	tests := []struct {
		name string
		body []byte
		want string
	}{
		{"not json", []byte("{not json"), "config is not a valid Xray config"},
		{"no api inbound", []byte(`{"inbounds":[]}`), `no inbound tagged "api"`},
		{"the core's own test fails", f.config(markerFailTest).compact(t), "the core rejected the config: Failed to start: fake rejects this config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.apply(tt.body)
			wantConfigError(t, err, tt.want)

			if got := f.starts(); got != 1 {
				t.Fatalf("core started %d times, want the original run only", got)
			}
			f.wantRevision(good, false)
			f.wantSavedBody(good)
			snap := f.core.Snapshot()
			if snap.XrayState != agentproto.XrayStateRunning || !strings.Contains(snap.XrayError, "not applied") {
				t.Fatalf("snapshot = %+v, want running with the refusal reported", snap)
			}
		})
	}

	if resp := f.mustApply(good); resp.Applied != agentproto.AppliedNoop {
		t.Fatalf("a later good push = %+v, want noop", resp)
	}
	if snap := f.core.Snapshot(); snap.XrayError != "" {
		t.Fatalf("the refusal must clear once a push succeeds, got %q", snap.XrayError)
	}
}

func TestApplyGoesBackWhenTheCoreDoesNotStart(t *testing.T) {
	f := newFixture(t)
	good := f.config("a").compact(t)
	f.mustApply(good)
	pid := f.pid()

	_, err := f.apply(f.config(markerFailStart).compact(t))
	wantConfigError(t, err, "xray exited right after it started: Failed to start: fake cannot bind")

	f.wantRevision(good, false)
	f.wantSavedBody(good)
	snap := f.core.Snapshot()
	if snap.XrayState != agentproto.XrayStateRunning || !strings.Contains(snap.XrayError, "fake cannot bind") {
		t.Fatalf("snapshot = %+v, want the old config running and the failure reported", snap)
	}
	if got := f.starts(); got != 2 || f.pid() == pid {
		t.Fatalf("core started %d times, want the original run and the rollback", got)
	}
}

func TestApplyOfAFirstConfigThatDoesNotStartLeavesNothingRunning(t *testing.T) {
	f := newFixture(t)

	_, err := f.apply(f.config(markerFailStart).compact(t))
	wantConfigError(t, err, "fake cannot bind")

	snap := f.core.Snapshot()
	if snap.XrayState != agentproto.XrayStateError || snap.Revision != "" {
		t.Fatalf("snapshot = %+v, want an error state with no config", snap)
	}
	if saved, err := f.state.LastGood(); saved != nil || err != nil {
		t.Fatalf("last good = %v, %v; a config that never ran must not be kept", saved, err)
	}
}

// The core opens the log files the agent chose, possibly while it is being tested, so the
// folder has to exist before either.
func TestApplyCreatesTheLogFolder(t *testing.T) {
	f := newFixture(t)
	logDir := filepath.Join(t.TempDir(), "not", "yet", "there")
	core := NewCore(f.state, logDir)
	t.Cleanup(core.Close)

	if _, err := core.Apply(context.Background(), f.config("a").compact(t), false); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if info, err := os.Stat(logDir); err != nil || !info.IsDir() {
		t.Fatalf("log folder = %v, %v; want it created", info, err)
	}
	raw, err := os.ReadFile(filepath.Join(f.binDir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(logDir, "access.log"); !strings.Contains(string(raw), strings.ReplaceAll(want, `\`, `\\`)) {
		t.Fatalf("the core's config does not point its access log at %s:\n%s", want, raw)
	}
}

func TestBootReportsADamagedLastGoodConfig(t *testing.T) {
	f := newFixture(t)
	if err := os.WriteFile(filepath.Join(f.state.dir, lastGoodFile), []byte("{torn"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := f.core.Boot(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unreadable") {
		t.Fatalf("Boot = %v, want the unreadable file reported", err)
	}
	snap := f.core.Snapshot()
	if snap.XrayState != agentproto.XrayStateError || !strings.Contains(snap.XrayError, "unreadable") {
		t.Fatalf("snapshot = %+v, want an error state that says why the node is idle", snap)
	}
}

// A config that runs but could not be stored is not a failed push: the status keeps saying
// the core is running, and the next push of the same config stores it.
func TestApplyOfAConfigThatRunsButCannotBeSaved(t *testing.T) {
	f := newFixture(t)
	body := f.config("a").compact(t)
	blocker := filepath.Join(f.state.dir, lastGoodFile)
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := f.apply(body)
	var unsaved *SaveError
	if !errors.As(err, &unsaved) {
		t.Fatalf("Apply = %v, want a *SaveError", err)
	}
	snap := f.core.Snapshot()
	if snap.XrayState != agentproto.XrayStateRunning || !strings.Contains(snap.XrayError, "is running but not saved as the last good config") {
		t.Fatalf("snapshot = %+v, want the core running and the unsaved config reported", snap)
	}
	if strings.Contains(snap.XrayError, "not applied") {
		t.Fatalf("XrayError = %q calls a live config not applied", snap.XrayError)
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	resp := f.mustApply(body)
	if resp.Applied != agentproto.AppliedNoop {
		t.Fatalf("retry = %+v, want noop", resp)
	}
	f.wantSavedBody(body)
	if snap := f.core.Snapshot(); snap.XrayError != "" {
		t.Fatalf("XrayError = %q after the config was saved, want it cleared", snap.XrayError)
	}
}

func TestBootStartsTheLastGoodConfig(t *testing.T) {
	f := newFixture(t)
	body := f.config("a").compact(t)
	f.mustApply(body)
	f.core.Close()

	next := NewCore(f.state, f.logDir)
	t.Cleanup(next.Close)
	if err := next.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if snap := next.Snapshot(); snap.XrayState != agentproto.XrayStateRunning || snap.Revision != agentproto.RevisionOf(body, false) {
		t.Fatalf("snapshot after boot = %+v, want the saved config running", snap)
	}

	resp, err := next.Apply(context.Background(), body, false)
	if err != nil || resp.Applied != agentproto.AppliedNoop {
		t.Fatalf("pushing the same config after boot = %+v, %v; want noop", resp, err)
	}
	if got := f.starts(); got != 2 {
		t.Fatalf("core started %d times, want the first run and the boot", got)
	}
}

func TestBootWithNothingSavedDoesNothing(t *testing.T) {
	f := newFixture(t)
	if err := f.core.Boot(context.Background()); err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if snap := f.core.Snapshot(); snap.XrayState != agentproto.XrayStateStopped {
		t.Fatalf("state = %q, want stopped", snap.XrayState)
	}
}

func TestSupervisorStartsACrashedCoreAgain(t *testing.T) {
	f := newFixture(t)
	body := f.config("a").compact(t)
	f.mustApply(body)
	pid := f.pid()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.core.Run(ctx)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 10*time.Second, "the core to be started again", func() bool {
		return f.starts() == 2 && f.core.Snapshot().XrayState == agentproto.XrayStateRunning
	})
	if f.pid() == pid {
		t.Fatal("the core came back with the pid of the one that was killed")
	}
	f.wantRevision(body, false)
}

func TestClosedCoreIsNotBroughtBackBySupervisor(t *testing.T) {
	f := newFixture(t)
	f.mustApply(f.config("a").compact(t))
	f.core.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.core.Run(ctx)
	time.Sleep(10 * superviseEvery)

	if got := f.starts(); got != 1 {
		t.Fatalf("core started %d times after Close, want 1", got)
	}
}

func TestRestart(t *testing.T) {
	f := newFixture(t)
	if err := f.core.Restart(context.Background()); !errors.Is(err, ErrNoConfig) {
		t.Fatalf("Restart before any config = %v, want ErrNoConfig", err)
	}

	body := f.config("a").compact(t)
	f.mustApply(body)
	pid := f.pid()
	if err := f.core.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if got := f.starts(); got != 2 || f.pid() == pid {
		t.Fatalf("core started %d times, want a second process", got)
	}
	f.wantRevision(body, false)
}

func TestStatsOfACoreThatIsNotRunning(t *testing.T) {
	f := newFixture(t)
	stats, err := f.core.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.XrayStartedAt != 0 || stats.Inbounds == nil || stats.Users == nil || stats.Online == nil {
		t.Fatalf("stats = %+v, want zero start time and empty, non-nil collections", stats)
	}
}
