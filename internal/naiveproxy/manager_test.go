package naiveproxy

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

// fakeChildListenPort reads the port renderCaddyfile wrote into configPath's
// site address, so each fake child in a test binds its own port, not a shared one.
var caddyfileSitePort = regexp.MustCompile(`https://:(\d+)\s*\{`)

func fakeChildListenPort(configPath string) (int, bool) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return 0, false
	}
	m := caddyfileSitePort.FindSubmatch(data)
	if m == nil {
		return 0, false
	}
	port, err := strconv.Atoi(string(m[1]))
	return port, err == nil
}

// replayFakeOutput has the fake child write path's contents to w once it is
// serving, then drop <path>.sent so a test knows the bytes are in the pipe.
func replayFakeOutput(w *os.File, path string) {
	if path == "" {
		return
	}
	if data, err := os.ReadFile(path); err == nil {
		_, _ = w.Write(data)
	}
	_ = os.WriteFile(path+".sent", nil, 0o644)
}

// TestMain re-execs the test binary as a fake caddy child (NAIVE_FAKE_CHILD=1):
// records its pid, listens on its --config file's own port, blocks. Mirrors internal/mtproto.
func TestMain(m *testing.M) {
	if os.Getenv("NAIVE_FAKE_CHILD") == "1" {
		if f, err := os.OpenFile(os.Getenv("NAIVE_FAKE_PIDFILE"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
		}
		// NAIVE_FAKE_STDOUT_ON_TERM_FILE makes it flush that file to stdout on SIGTERM and exit,
		// like a Caddy whose last tunnels finish while it drains before shutting down.
		if termFile := os.Getenv("NAIVE_FAKE_STDOUT_ON_TERM_FILE"); termFile != "" {
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGTERM)
			go func() {
				<-sigs
				replayFakeOutput(os.Stdout, termFile)
				os.Exit(0)
			}()
		}
		// NAIVE_FAKE_NEVER_READY simulates a process that starts (and gets a
		// pid recorded above) but never opens its listener -- WaitReady times out.
		if os.Getenv("NAIVE_FAKE_NEVER_READY") == "1" {
			select {}
		}
		for i, arg := range os.Args {
			if arg == "--config" && i+1 < len(os.Args) {
				if port, ok := fakeChildListenPort(os.Args[i+1]); ok {
					if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
						go func() {
							for {
								c, err := ln.Accept()
								if err != nil {
									return
								}
								c.Close()
							}
						}()
					}
				}
			}
		}
		replayFakeOutput(os.Stderr, os.Getenv("NAIVE_FAKE_STDERR_FILE"))
		replayFakeOutput(os.Stdout, os.Getenv("NAIVE_FAKE_STDOUT_FILE"))
		if exitFile := os.Getenv("NAIVE_FAKE_EXIT_FILE"); exitFile != "" {
			for {
				if _, err := os.Stat(exitFile); err == nil {
					os.Exit(1)
				} else if !os.IsNotExist(err) {
					os.Exit(2)
				}
				time.Sleep(time.Millisecond)
			}
		}
		select {}
	}
	os.Exit(m.Run())
}

// installFakeCaddy points BinPath() at a copy of this test binary and arms
// TestMain's fake-child mode -- no real, linux/amd64-only Caddy binary needed.
func installFakeCaddy(t *testing.T) (pidFile string) {
	t.Helper()
	binDir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test binary: %v", err)
	}
	payload, err := os.ReadFile(self)
	if err != nil {
		t.Fatalf("read test binary: %v", err)
	}
	pidFile = filepath.Join(binDir, "caddy-pids.txt")
	t.Setenv("XUI_BIN_FOLDER", binDir)
	// BinPath() reads XUI_BIN_FOLDER, so it must be set before calling it;
	// Dir() is a subdirectory of binDir that t.TempDir() didn't create.
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatalf("create %s: %v", Dir(), err)
	}
	if err := os.WriteFile(BinPath(), payload, 0o755); err != nil {
		t.Fatalf("install fake caddy: %v", err)
	}
	t.Setenv("NAIVE_FAKE_CHILD", "1")
	t.Setenv("NAIVE_FAKE_PIDFILE", pidFile)
	return pidFile
}

func spawnCount(t *testing.T, pidFile string) int {
	t.Helper()
	data, err := os.ReadFile(pidFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	return len(strings.Fields(string(data)))
}

func waitSpawnCount(t *testing.T, pidFile string, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := spawnCount(t, pidFile)
		if got == want {
			return
		}
		if got > want {
			t.Fatalf("expected %d spawn(s), got %d", want, got)
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected %d spawn(s), still %d after timeout", want, got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// freeLoopbackPort asks the OS for a port and releases it -- a tiny race
// against the fake child's own bind, same as this pattern elsewhere here.
func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testInst(t *testing.T, id int, usernames ...string) Instance {
	t.Helper()
	inst := Instance{
		Id:         id,
		ListenAddr: fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t)),
		Domain:     "decoy.example.test",
		CertFile:   "/tmp/cert.pem",
		KeyFile:    "/tmp/key.pem",
	}
	for _, u := range usernames {
		inst.Clients = append(inst.Clients, Client{Email: u + "@x", Username: u, Password: "pw-" + u})
	}
	return inst
}

func newTestManager() *Manager {
	return &Manager{procs: map[int]*managed{}}
}

func TestEnsureStartsAProcess(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)
	if !m.IsRunning(1) {
		t.Error("IsRunning(1) = false after Ensure started it")
	}
}

// Until the engine is installed every NaiveProxy inbound hits this: the error
// must say so, and nothing may be written for an instance that cannot start.
func TestEnsureReportsAMissingEngine(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	m := newTestManager()

	err := m.Ensure(testInst(t, 1, "alice"))
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Ensure without the binary = %v, want ErrNotInstalled", err)
	}
	if m.IsRunning(1) {
		t.Fatal("IsRunning(1) = true although nothing could start")
	}
	if _, statErr := os.Stat(Dir()); !os.IsNotExist(statErr) {
		t.Errorf("%s created for an instance that could not start: %v", Dir(), statErr)
	}
}

// The engine check has to come before the first write: where the folder cannot even
// be created, a missing engine must still be reported as that, not as a mkdir error.
func TestEnsureReportsAMissingEngineBeforeTouchingTheDisk(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XUI_BIN_FOLDER", filepath.Join(blocker, "bin"))
	m := newTestManager()

	if err := m.Ensure(testInst(t, 1, "alice")); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Ensure with an uncreatable bin folder = %v, want ErrNotInstalled", err)
	}
}

// The files written ahead of a spawn must not be left behind when it fails:
// nothing tracks them, so removeLocked would never clean them up.
func TestEnsureCleansUpAfterAFailedStart(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// A regular file is "installed" as far as IsInstalled can tell, but exec cannot run it.
	if err := os.WriteFile(BinPath(), []byte("not an executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestManager()

	err := m.Ensure(testInst(t, 1, "alice"))
	if err == nil || errors.Is(err, ErrNotInstalled) {
		t.Fatalf("Ensure with an engine that cannot run = %v, want the start error", err)
	}
	if _, statErr := os.Stat(configPathForID(1)); !os.IsNotExist(statErr) {
		t.Errorf("Caddyfile left behind after a failed start: %v", statErr)
	}
	if _, statErr := os.Stat(decoyDirForID(1)); !os.IsNotExist(statErr) {
		t.Errorf("decoy dir left behind after a failed start: %v", statErr)
	}
}

// The check only gates a spawn: an instance already running is left alone when the
// binary goes missing, exactly as before the check existed.
func TestEnsureLeavesARunningInstanceAloneWhenTheEngineVanishes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a running executable cannot be removed on Windows")
	}
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	if err := os.Remove(BinPath()); err != nil {
		t.Fatal(err)
	}
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure of a running instance after the engine vanished = %v, want nil", err)
	}
	if !m.IsRunning(1) {
		t.Error("the running instance was stopped when the engine vanished")
	}
}

func TestEnsureIsNoopWhenNothingChanged(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got := spawnCount(t, pidFile); got != 1 {
		t.Errorf("spawn count = %d after an identical Ensure, want 1 (no restart)", got)
	}
}

func TestEnsureRestartsWhenClientsChange(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	inst.Clients = append(inst.Clients, Client{Email: "bob@x", Username: "bob", Password: "pw-bob"})
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	waitSpawnCount(t, pidFile, 2)
}

// A client-less instance must not run at all: an unauthenticated
// forward_proxy is a live security hole, not just an idle process.
func TestEnsureStopsWhenClientsBecomeEmpty(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	waitSpawnCount(t, pidFile, 1)

	inst.Clients = nil
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure with no clients: %v", err)
	}
	if m.IsRunning(1) {
		t.Error("IsRunning(1) = true after Ensure with zero clients")
	}
}

func TestEnsureNeverStartsAClientlessInstance(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1) // no clients

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := spawnCount(t, pidFile); got != 0 {
		t.Errorf("spawn count = %d for a client-less instance, want 0", got)
	}
}

// The decoy file must exist by the time Ensure returns, not eventually.
func TestEnsureWritesDecoyContent(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)

	body, err := os.ReadFile(decoyDirForID(1) + "/index.html")
	if err != nil {
		t.Fatalf("decoy index.html: %v", err)
	}
	if len(body) == 0 {
		t.Error("decoy index.html is empty")
	}
}

// A domain-only change doesn't touch the fingerprint (decoy dir is keyed by
// Id) -- confirms writeDecoyContent's placement ahead of that check matters.
func TestEnsureRefreshesDecoyContentWhenOnlyDomainChanges(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	first, err := os.ReadFile(decoyDirForID(1) + "/index.html")
	if err != nil {
		t.Fatalf("decoy index.html: %v", err)
	}

	inst.Domain = "a-completely-different-domain.example.test"
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	second, err := os.ReadFile(decoyDirForID(1) + "/index.html")
	if err != nil {
		t.Fatalf("decoy index.html after domain change: %v", err)
	}
	if string(first) == string(second) {
		t.Error("decoy content unchanged after Domain changed -- seed not actually refreshed")
	}
}

func TestRemoveDeletesDecoyContent(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if _, err := os.Stat(decoyDirForID(1)); err != nil {
		t.Fatalf("setup: decoy dir missing right after Ensure: %v", err)
	}

	m.Remove(1)
	if _, err := os.Stat(decoyDirForID(1)); !os.IsNotExist(err) {
		t.Errorf("decoy dir still exists after Remove: err = %v", err)
	}
}

func TestRemoveStopsTheProcess(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !m.IsRunning(1) {
		t.Fatal("setup: IsRunning(1) = false right after Ensure")
	}

	m.Remove(1)
	if m.IsRunning(1) {
		t.Error("IsRunning(1) = true after Remove")
	}
}

// The job's own cadence gates a costly frontproxy reload on this return
// value, so a false positive/negative here is a real, not just cosmetic, bug.
func TestReconcileReportsWhetherAnythingChanged(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst1 := testInst(t, 1, "alice")

	if changed := m.Reconcile([]Instance{inst1}); !changed {
		t.Error("first Reconcile: changed = false, want true (a process started)")
	}
	t.Cleanup(m.StopAll)

	if changed := m.Reconcile([]Instance{inst1}); changed {
		t.Error("identical Reconcile: changed = true, want false")
	}

	if changed := m.Reconcile(nil); !changed {
		t.Error("Reconcile with nothing desired: changed = false, want true (a process stopped)")
	}
}

// A never-ready instance must not report changed every retry tick, or a
// broken config forces an unconditional frontproxy reload every 10s forever.
func TestReconcileDoesNotReportChangedForAnInstanceThatNeverBecomesReady(t *testing.T) {
	installFakeCaddy(t)
	t.Setenv("NAIVE_FAKE_NEVER_READY", "1")
	oldTimeout := startupTimeout
	startupTimeout = 200 * time.Millisecond
	t.Cleanup(func() { startupTimeout = oldTimeout })

	m := newTestManager()
	inst := testInst(t, 1, "alice")

	if changed := m.Reconcile([]Instance{inst}); changed {
		t.Error("first Reconcile (spawn never becomes ready): changed = true, want false")
	}
	if changed := m.Reconcile([]Instance{inst}); changed {
		t.Error("retry Reconcile (still never ready): changed = true, want false")
	}
}

func TestReconcileStopsWhatIsNoLongerDesired(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst1 := testInst(t, 1, "alice")
	inst2 := testInst(t, 2, "bob")

	m.Reconcile([]Instance{inst1, inst2})
	t.Cleanup(m.StopAll)
	if !m.IsRunning(1) || !m.IsRunning(2) {
		t.Fatal("setup: both instances should be running after the first Reconcile")
	}

	m.Reconcile([]Instance{inst1})
	if !m.IsRunning(1) {
		t.Error("IsRunning(1) = false after a Reconcile that still wants it")
	}
	if m.IsRunning(2) {
		t.Error("IsRunning(2) = true after a Reconcile that no longer wants it")
	}
}

// warningCount counts the warnings in the panel log that contain needle.
func warningCount(needle string) int {
	n := 0
	for _, line := range logger.GetLogs(1000, "warning") {
		if strings.Contains(line, needle) {
			n++
		}
	}
	return n
}

// Reconcile runs every 10 s, so a failure that lasts (most often the engine not being
// installed) must be logged once, not once a tick.
func TestReconcileLogsAPersistentFailureOnce(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	m := newTestManager()
	const id = 920001
	inst := testInst(t, id, "alice")
	needle := fmt.Sprintf("reconcile failed for inbound %d:", id)

	for range 3 {
		m.Reconcile([]Instance{inst})
	}
	if got := warningCount(needle); got != 1 {
		t.Fatalf("a missing engine was logged %d times over three ticks, want once", got)
	}
}

func TestReconcileLogsAChangedFailureAgain(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	m := newTestManager()
	const id = 920002
	inst := testInst(t, id, "alice")
	needle := fmt.Sprintf("reconcile failed for inbound %d:", id)

	m.Reconcile([]Instance{inst})
	inst.ListenAddr = "not-a-valid-addr"
	m.Reconcile([]Instance{inst})
	m.Reconcile([]Instance{inst})
	if got := warningCount(needle); got != 2 {
		t.Fatalf("two different failures were logged %d times, want 2 (each once)", got)
	}
}

// A failure that was fixed and then comes back is news again.
func TestReconcileLogsAFailureAgainAfterItWasFixed(t *testing.T) {
	pidFile := installFakeCaddy(t)
	off := BinPath() + ".off"
	if err := os.Rename(BinPath(), off); err != nil {
		t.Fatal(err)
	}
	m := newTestManager()
	const id = 920003
	inst := testInst(t, id, "alice")
	needle := fmt.Sprintf("reconcile failed for inbound %d:", id)

	m.Reconcile([]Instance{inst})
	if got := warningCount(needle); got != 1 {
		t.Fatalf("missing engine logged %d times, want once", got)
	}

	if err := os.Rename(off, BinPath()); err != nil {
		t.Fatal(err)
	}
	m.Reconcile([]Instance{inst})
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	m.StopAll()
	if err := os.Rename(BinPath(), off); err != nil {
		t.Fatal(err)
	}
	m.Reconcile([]Instance{inst})
	if got := warningCount(needle); got != 2 {
		t.Fatalf("the failure that came back was logged %d times in all, want 2", got)
	}
}

// An inbound dropped from the desired set takes its failure with it, so one
// added back later is reported afresh rather than silenced by a stale entry.
func TestReconcileForgetsTheFailureOfADroppedInbound(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	m := newTestManager()
	const id = 920004
	inst := testInst(t, id, "alice")
	needle := fmt.Sprintf("reconcile failed for inbound %d:", id)

	m.Reconcile([]Instance{inst})
	m.Reconcile(nil)
	m.Reconcile([]Instance{inst})
	if got := warningCount(needle); got != 2 {
		t.Fatalf("an inbound removed and added back was logged %d times in all, want 2", got)
	}
	if len(m.failing) != 1 {
		t.Errorf("failing = %v, want only the inbound that is still desired", m.failing)
	}
}

func TestStopAllStopsEveryProcess(t *testing.T) {
	installFakeCaddy(t)
	m := newTestManager()
	inst1 := testInst(t, 1, "alice")
	inst2 := testInst(t, 2, "bob")
	m.Reconcile([]Instance{inst1, inst2})
	if !m.IsRunning(1) || !m.IsRunning(2) {
		t.Fatal("setup: both instances should be running")
	}

	m.StopAll()
	if m.IsRunning(1) || m.IsRunning(2) {
		t.Error("an instance is still running after StopAll")
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stageFakeOutput sets what the fake child replays on its stdout and stderr,
// and returns the stdout file so a test can wait for its ".sent" marker.
func stageFakeOutput(t *testing.T, stdout, stderr string) string {
	t.Helper()
	dir := t.TempDir()
	stdoutPath := filepath.Join(dir, "stdout.txt")
	stderrPath := filepath.Join(dir, "stderr.txt")
	if err := os.WriteFile(stdoutPath, []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stderrPath, []byte(stderr), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NAIVE_FAKE_STDOUT_FILE", stdoutPath)
	t.Setenv("NAIVE_FAKE_STDERR_FILE", stderrPath)
	return stdoutPath
}

// Stdout is the access log and nothing else: a line on stderr, however
// access-shaped, is operational output and must never be metered.
func TestCollectTrafficMetersTheChildsStdoutOnly(t *testing.T) {
	installFakeCaddy(t)
	// Unwanted lines come first: the stream is read in order, so once the last
	// wanted line is counted the unwanted ones before it have been rejected.
	stageFakeOutput(t,
		accessLine("invalid:eve@x", 9, 9)+"not json\n"+accessLine("alice@x", 100, 2000)+accessLine("bob@x", 5, 60)+accessLine("alice@x", 1, 2),
		accessLine("mallory@x", 999, 999))

	m := newTestManager()
	inst := testInst(t, 1, "alice", "bob")
	inst.Tag = "inbound-40100"
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)

	// GetResult is stderr's last line, so seeing it proves stderr was read.
	waitUntil(t, "the child's stderr line to be read", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return strings.Contains(m.procs[1].proc.GetResult(), "mallory@x")
	})

	sums := map[string]counters{}
	var online []string
	waitUntil(t, "the child's access lines to be metered", func() bool {
		deltas, on := m.CollectTraffic()
		for _, d := range deltas {
			if d.Tag != "inbound-40100" {
				t.Errorf("delta %+v has tag %q, want the inbound's own tag", d, d.Tag)
			}
			c := sums[d.Email]
			sums[d.Email] = counters{up: c.up + d.Up, down: c.down + d.Down}
		}
		online = append(online, on...)
		return sums["alice@x"].up == 101 && sums["bob@x"].up == 5
	})

	want := map[string]counters{"alice@x": {up: 101, down: 2002}, "bob@x": {up: 5, down: 60}}
	if len(sums) != len(want) || sums["alice@x"] != want["alice@x"] || sums["bob@x"] != want["bob@x"] {
		t.Errorf("metered %+v, want %+v", sums, want)
	}
	slices.Sort(online)
	if got := slices.Compact(online); !slices.Equal(got, []string{"alice@x", "bob@x"}) {
		t.Errorf("online = %v, want exactly the two users that moved bytes", got)
	}
}

// Bytes a Caddy reported while stopping (its open tunnels close then) must
// still reach the next CollectTraffic, though the process is already gone.
func TestCollectTrafficHandsOverWhatAStoppedProcessLeftBehind(t *testing.T) {
	installFakeCaddy(t)
	stdout := stageFakeOutput(t, accessLine("alice@x", 100, 2000), "")
	m := newTestManager()
	inst := testInst(t, 1, "alice")
	inst.Tag = "inbound-1"
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitUntil(t, "the child to replay its output", func() bool {
		_, err := os.Stat(stdout + ".sent")
		return err == nil
	})

	m.Remove(1) // returns only once the child has exited and its pipes are drained

	got, online := m.CollectTraffic()
	if len(got) != 1 || got[0] != (Traffic{Tag: "inbound-1", Email: "alice@x", Up: 100, Down: 2000}) {
		t.Fatalf("CollectTraffic after Remove = %+v, want the stopped process's last line", got)
	}
	if !slices.Equal(online, []string{"alice@x"}) {
		t.Errorf("online = %v, want [alice@x]", online)
	}
	if again, _ := m.CollectTraffic(); len(again) != 0 {
		t.Errorf("second CollectTraffic = %+v, want nothing", again)
	}
	if len(m.meters) != 0 {
		t.Errorf("%d meter(s) kept for an inbound with no process and nothing left to hand over", len(m.meters))
	}
}

func TestCollectTrafficKeepsTheMeterOfARunningProcess(t *testing.T) {
	m := newTestManager()
	m.meters = map[int]*meter{1: newMeter("a"), 2: newMeter("b")}
	m.procs[2] = &managed{proc: &Process{}}
	feed(t, m.meters[1], accessLine("x@x", 1, 1))
	feed(t, m.meters[2], accessLine("y@x", 1, 1))

	if got, _ := m.CollectTraffic(); len(got) != 2 {
		t.Fatalf("CollectTraffic = %+v, want both inbounds' deltas", got)
	}
	if _, ok := m.meters[1]; ok {
		t.Error("meter of an inbound with no process was kept after being drained")
	}
	if _, ok := m.meters[2]; !ok {
		t.Error("meter of a tracked process was dropped; its next tunnels would go unmetered")
	}
}

// Tag is not part of the Caddyfile, so a tag-only change must not restart
// Caddy (it would drop every tunnel) -- yet traffic must roll up to the new tag.
func TestEnsureFollowsATagChangeWithoutRestartingCaddy(t *testing.T) {
	pidFile := installFakeCaddy(t)
	m := newTestManager()
	inst := testInst(t, 1, "alice")
	inst.Tag = "inbound-1"
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("first Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)
	waitSpawnCount(t, pidFile, 1)

	inst.Tag = "inbound-2"
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
	if got := spawnCount(t, pidFile); got != 1 {
		t.Errorf("spawn count = %d after a tag-only change, want 1 (no restart)", got)
	}
	feed(t, m.meters[1], accessLine("alice@x", 1, 1))
	if got := m.meters[1].drain(); len(got) != 1 || got[0].Tag != "inbound-2" {
		t.Errorf("drain = %+v, want it rolled up under inbound-2", got)
	}
}

// A routed inbound that leaves the desired set is stopped and its last lines are drained
// afterwards; they must still say the Xray bridge already counted them.
func TestCollectTrafficKeepsTheRoutedLabelOfAStoppedProcess(t *testing.T) {
	installFakeCaddy(t)
	stdout := stageFakeOutput(t, accessLine("alice@x", 100, 2000), "")
	m := newTestManager()
	inst := testInst(t, 1, "alice")
	inst.Tag = "inbound-1"
	inst.RouteThroughXray, inst.XrayRoutePort = true, 50000
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	waitUntil(t, "the child to replay its output", func() bool {
		_, err := os.Stat(stdout + ".sent")
		return err == nil
	})

	m.Remove(1)

	got, _ := m.CollectTraffic()
	want := []Traffic{{Tag: "inbound-1", Email: "alice@x", Routed: true, Up: 100, Down: 2000}}
	if !slices.Equal(got, want) {
		t.Fatalf("CollectTraffic after Remove = %+v, want %+v", got, want)
	}
}

// A draining Caddy still logs the tunnels that finish before it exits, so a routing switch must
// label those lines with the routing they ran under, not the one that replaces it.
func TestEnsureLabelsAStoppingProcessLinesWithTheOldRouting(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake child flushes on SIGTERM, which Windows cannot deliver")
	}
	installFakeCaddy(t)
	term := filepath.Join(t.TempDir(), "on-term.txt")
	if err := os.WriteFile(term, []byte(accessLine("alice@x", 100, 2000)), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NAIVE_FAKE_STDOUT_ON_TERM_FILE", term)
	m := newTestManager()
	inst := testInst(t, 1, "alice")
	inst.Tag = "inbound-1"
	inst.RouteThroughXray, inst.XrayRoutePort = true, 50000
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Cleanup(m.StopAll)

	inst.RouteThroughXray = false // restarts Caddy: the routed process flushes its line, then an unrouted one starts
	if err := m.Ensure(inst); err != nil {
		t.Fatalf("Ensure after the switch: %v", err)
	}

	got, _ := m.CollectTraffic()
	want := []Traffic{{Tag: "inbound-1", Email: "alice@x", Routed: true, Up: 100, Down: 2000}}
	if !slices.Equal(got, want) {
		t.Fatalf("CollectTraffic after the switch = %+v, want the flushed line labelled routed: %+v", got, want)
	}
}
