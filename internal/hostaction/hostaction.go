// Package hostaction runs commands that change the host as a transient systemd unit of their own, outside the panel's
// read-only sandbox, and reports how they ended.
package hostaction

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	// modeEnv forces how commands start: "direct" as a child of this process, "unit" as a transient unit.
	modeEnv           = "XUI_HOSTACTION"
	defaultMaxRuntime = 2 * time.Hour
	pollEvery         = 100 * time.Millisecond
	slowPollEvery     = time.Second
	watchMargin       = 10 * time.Minute
	maxStdout         = 4 << 20
	maxStderr         = 64 << 10
	errorTail         = 4000
)

const (
	notBootedMarker = "System has not been booted with systemd"
	noBusMarker     = "Failed to connect to bus"
)

// SystemdUnavailable is whether the output of a failed systemd-run says there is no systemd to start a unit with.
func SystemdUnavailable(output string) bool {
	return strings.Contains(output, notBootedMarker) || strings.Contains(output, noBusMarker)
}

// aliveEvery is how often a unit is asked whether it still runs, in nanoseconds; atomic because watchers read it.
var aliveEvery atomic.Int64

func init() { aliveEvery.Store(int64(3 * time.Second)) }

// Spec is one command to run.
type Spec struct {
	// Name says what runs, for messages and the unit name.
	Name string
	// Argv is the command and its arguments, run without a shell.
	Argv []string
	// Stdin is fed to the command; empty means /dev/null.
	Stdin string
	// Env is extra NAME=value settings: a transient unit starts without any of the panel's.
	Env []string
	// Stage is content the command reads from $HOSTACTION_DIR/stage; nil means none.
	Stage []byte
	// StopOnCancel stops the command and what it started when ctx ends, instead of leaving it to finish.
	StopOnCancel bool
	// MaxRuntime caps how long a unit may run; zero means two hours.
	MaxRuntime time.Duration
}

// Result is what a command wrote, once it ended or was stopped.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// ExitError is a command that ran and failed. Code is -1 when it left no exit status, so it was stopped or killed.
type ExitError struct {
	Name   string
	Code   int
	Stderr string
}

func (e *ExitError) Error() string {
	text := fmt.Sprintf("%s: exit status %d", e.Name, e.Code)
	if e.Code < 0 {
		text = e.Name + ": ended without an exit status, so it was stopped or killed"
	}
	if e.Stderr != "" {
		text += ": " + e.Stderr
	}
	return text
}

// DetachedError is a command that outlived its context and goes on without the caller.
type DetachedError struct {
	Name  string
	Where string
	Err   error
}

func (e *DetachedError) Error() string {
	return fmt.Sprintf("%s: %v; it is still running %s", e.Name, e.Err, e.Where)
}

func (e *DetachedError) Unwrap() error { return e.Err }

var (
	// ErrSandboxed means the panel's own sandbox forbids the action and no transient unit can be started for it.
	ErrSandboxed       = errors.New("this panel runs in a read-only systemd sandbox and cannot start a unit of its own, so it cannot change the host; run the command by hand")
	errSystemdUnusable = errors.New("systemd is not usable here")
	runSeq             atomic.Uint64
	// active holds the folders of the runs this process owns, which Recover must leave alone.
	active sync.Map
)

// launcher starts the script of a run and tells what became of it.
type launcher interface {
	start(ctx context.Context, r *run) error
	alive(r *run) bool
	stop(r *run)
	release(r *run)
}

// run is one command in flight; its files live in dir.
type run struct {
	spec      Spec
	dir       string
	unit      string
	pid       int
	exited    chan struct{}
	collected bool
	started   time.Time
}

type meta struct {
	Name       string `json:"name"`
	Unit       string `json:"unit,omitempty"`
	PID        int    `json:"pid,omitempty"`
	Started    int64  `json:"started"`
	MaxRuntime int64  `json:"maxRuntime"`
}

// Run starts spec and waits for it until ctx ends. A command that changes the host is then left to finish and the
// error is a *DetachedError; with StopOnCancel it is stopped instead.
func Run(ctx context.Context, spec Spec) (Result, error) {
	if err := spec.validate(); err != nil {
		return Result{}, err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("%s: %w", spec.Name, err)
	}
	r, err := newRun(spec)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", spec.Name, err)
	}
	l, err := r.begin(ctx)
	if err != nil {
		r.discard()
		return Result{}, fmt.Errorf("%s: %w", spec.Name, err)
	}
	r.saveMeta()
	return r.await(ctx, l)
}

// validate refuses a spec with no command, which would otherwise run an empty script and report success.
func (s Spec) validate() error {
	if s.Name == "" || len(s.Argv) == 0 || s.Argv[0] == "" {
		return fmt.Errorf("hostaction: %q has no command to run", s.Name)
	}
	return nil
}

func (s Spec) maxRuntime() time.Duration {
	if s.MaxRuntime > 0 {
		return s.MaxRuntime
	}
	return defaultMaxRuntime
}

// baseDir is where the runs keep their files: a subfolder of the panel's bin folder, which its unit may write.
func baseDir() (string, error) {
	return filepath.Abs(filepath.Join(config.GetBinFolderPath(), "hostaction"))
}

func newRun(spec Spec) (*run, error) {
	base, err := baseDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir := filepath.Join(base, strconv.FormatInt(time.Now().UnixNano(), 36)+"-"+strconv.FormatUint(runSeq.Add(1), 36))
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	r := &run{spec: spec, dir: dir, started: time.Now()}
	active.Store(dir, struct{}{})
	if err := r.prepare(); err != nil {
		r.discard()
		return nil, err
	}
	return r, nil
}

func (r *run) path(name string) string { return filepath.Join(r.dir, name) }

func (r *run) prepare() error {
	if err := os.WriteFile(r.path("run.sh"), []byte(r.script()), 0o700); err != nil {
		return err
	}
	if r.spec.Stdin != "" {
		if err := os.WriteFile(r.path("stdin"), []byte(r.spec.Stdin), 0o600); err != nil {
			return err
		}
	}
	if r.spec.Stage != nil {
		return os.WriteFile(r.path("stage"), r.spec.Stage, 0o600)
	}
	return nil
}

// begin starts the script: as a transient unit where systemd allows it, otherwise as a child of this process.
func (r *run) begin(ctx context.Context) (launcher, error) {
	switch os.Getenv(modeEnv) {
	case "direct":
		return r.beginDirect(ctx)
	case "unit":
		return r.beginUnit(ctx)
	}
	if systemdUsable() {
		l, err := r.beginUnit(ctx)
		if !errors.Is(err, errSystemdUnusable) {
			return l, err
		}
		logger.Warning("hostaction: systemd-run is not usable here, so this command runs as a child of the panel:", err)
	}
	return r.beginDirect(ctx)
}

func (r *run) beginUnit(ctx context.Context) (launcher, error) {
	r.unit = "x-ui-hostaction-" + label(r.spec.Name) + "-" + filepath.Base(r.dir)
	if err := (unitLauncher{}).start(ctx, r); err != nil {
		r.unit = ""
		return nil, err
	}
	logger.Infof("hostaction: %s runs as unit %s", r.spec.Name, r.unit)
	return unitLauncher{}, nil
}

func (r *run) beginDirect(ctx context.Context) (launcher, error) {
	if readOnlyUsr() {
		return nil, ErrSandboxed
	}
	if err := (directLauncher{}).start(ctx, r); err != nil {
		return nil, err
	}
	return directLauncher{}, nil
}

func (r *run) launcher() launcher {
	if r.unit != "" {
		return unitLauncher{}
	}
	return directLauncher{}
}

func (r *run) await(ctx context.Context, l launcher) (Result, error) {
	if !r.ended(ctx, l) {
		if r.spec.StopOnCancel {
			l.stop(r)
			res, _ := r.collect() // what it wrote before it was stopped
			r.finish(l)
			return res, fmt.Errorf("%s: %w", r.spec.Name, ctx.Err())
		}
		go r.watch(l)
		return Result{}, r.detached(ctx.Err())
	}
	res, err := r.collect()
	r.finish(l)
	return res, err
}

func (r *run) detached(cause error) error {
	where := "in the background, and it ends if the panel's service is restarted"
	if r.unit != "" {
		where = "as unit " + r.unit + " and goes on without the panel"
	}
	return &DetachedError{Name: r.spec.Name, Where: where, Err: cause}
}

// ended waits until the command has recorded how it ended or is gone, and says false when ctx ends first.
func (r *run) ended(ctx context.Context, l launcher) bool {
	began := time.Now()
	checked := began
	timer := time.NewTimer(pollEvery)
	defer timer.Stop()
	for !r.hasStatus() {
		select {
		case <-ctx.Done():
			return r.hasStatus()
		case <-r.exited:
			return true
		case <-timer.C:
			if time.Since(checked) >= time.Duration(aliveEvery.Load()) {
				checked = time.Now()
				if !l.alive(r) {
					return true
				}
			}
			next := pollEvery
			if time.Since(began) > 5*time.Second {
				next = slowPollEvery
			}
			timer.Reset(next)
		}
	}
	return true
}

// watch records how a command that nobody waits for ended, and only then drops its files.
func (r *run) watch(l launcher) {
	ctx, cancel := context.WithDeadline(context.Background(), r.started.Add(r.spec.maxRuntime()+watchMargin))
	defer cancel()
	if !r.ended(ctx, l) {
		active.Delete(r.dir)
		logger.Warningf("hostaction: %s was still running when the panel gave up watching it; its files stay in %s", r.spec.Name, r.dir)
		return
	}
	r.report(l)
}

func (r *run) report(l launcher) {
	if _, err := r.collect(); err != nil {
		logger.Warningf("hostaction: %v (it ended after the panel had stopped waiting for it)", err)
	} else {
		logger.Infof("hostaction: %s finished after the panel had stopped waiting for it", r.spec.Name)
	}
	r.finish(l)
}

func (r *run) finish(l launcher) {
	l.release(r)
	r.discard()
}

func (r *run) discard() {
	active.Delete(r.dir)
	_ = os.RemoveAll(r.dir)
}

func (r *run) status() (int, bool) {
	raw, err := os.ReadFile(r.path("status"))
	if err != nil {
		return 0, false
	}
	code, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		return -1, true
	}
	return code, true
}

func (r *run) hasStatus() bool {
	_, ok := r.status()
	return ok
}

func (r *run) collect() (Result, error) {
	res := Result{Stdout: readHead(r.path("stdout"), maxStdout), Stderr: readTail(r.path("stderr"), maxStderr)}
	code, ok := r.status()
	if !ok {
		code = -1
	}
	if code == 0 {
		return res, nil
	}
	return res, &ExitError{Name: r.spec.Name, Code: code, Stderr: lastBytes(strings.TrimSpace(string(res.Stderr)), errorTail)}
}

func (r *run) saveMeta() {
	raw, err := json.Marshal(meta{Name: r.spec.Name, Unit: r.unit, PID: r.pid, Started: r.started.Unix(), MaxRuntime: int64(r.spec.maxRuntime().Seconds())})
	if err == nil {
		_ = os.WriteFile(r.path("meta.json"), raw, 0o600)
	}
}

func readHead(path string, limit int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out, _ := io.ReadAll(io.LimitReader(f, limit))
	return out
}

func readTail(path string, limit int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	offset := max(info.Size()-limit, 0)
	out, _ := io.ReadAll(io.NewSectionReader(f, offset, limit))
	return out
}

func lastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-n:], "")
}
