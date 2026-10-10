//go:build !windows

package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// fakeSystemdRun stands in for systemd-run: it records what it was asked, runs the command in the
// foreground as a unit would, and says 0 whatever that did, since --no-block only says it was queued.
const fakeSystemdRun = `#!/bin/sh
printf '%s\n' "$@" > "$FAKE_SYSTEMD_RUN_LOG"
if [ -n "$FAKE_SYSTEMD_RUN_FAIL" ]; then
    echo "$FAKE_SYSTEMD_RUN_FAIL" >&2
    exit 1
fi
while [ "$#" -gt 0 ]; do
    case "$1" in
        --unit) shift 2 ;;
        --*) shift ;;
        *) break ;;
    esac
done
"$@"
exit 0
`

// standInInstaller leaves the arguments it was run with in $STAND_IN_ARGS and exits with $STAND_IN_EXIT.
const standInInstaller = "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$STAND_IN_ARGS\"\nexit \"${STAND_IN_EXIT:-0}\"\n"

type updateFixture struct {
	*serverFixture
	installer  *httptest.Server
	hits       atomic.Int32
	script     atomic.Value // what the installer address answers with
	status     atomic.Int32 // and with which status
	runLog     string
	installerA string
}

func newUpdateFixture(t *testing.T) *updateFixture {
	t.Helper()
	f := &updateFixture{serverFixture: newServerFixture(t)}
	f.script.Store(standInInstaller)
	f.status.Store(http.StatusOK)
	f.installer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		w.WriteHeader(int(f.status.Load()))
		_, _ = w.Write([]byte(f.script.Load().(string)))
	}))
	t.Cleanup(f.installer.Close)

	previousURL, previousExe := installerURL, installedExecutable
	installerURL = f.installer.URL
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if installedExecutable, err = filepath.EvalSymlinks(exe); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { installerURL, installedExecutable = previousURL, previousExe })

	fakeBin := t.TempDir()
	if err := os.WriteFile(filepath.Join(fakeBin, "systemd-run"), []byte(fakeSystemdRun), 0o755); err != nil {
		t.Fatal(err)
	}
	f.runLog = filepath.Join(t.TempDir(), "systemd-run.args")
	f.installerA = filepath.Join(t.TempDir(), "installer.args")
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SYSTEMD_RUN_LOG", f.runLog)
	t.Setenv("STAND_IN_ARGS", f.installerA)
	return f
}

func (f *updateFixture) post(query string) reply {
	f.t.Helper()
	return f.authed(http.MethodPost, agentproto.PathUpdate+query, nil)
}

func (f *updateFixture) last() agentproto.UpdateStatus {
	f.t.Helper()
	return decode[agentproto.UpdateStatus](f.t, f.authed(http.MethodGet, agentproto.PathUpdate, nil), http.StatusOK)
}

func (f *updateFixture) scripts() []string {
	f.t.Helper()
	matches, err := filepath.Glob(filepath.Join(f.state.Dir(), "update-*.sh"))
	if err != nil {
		f.t.Fatal(err)
	}
	return matches
}

func (f *updateFixture) installerArgs() string {
	f.t.Helper()
	raw, err := os.ReadFile(f.installerA)
	if err != nil {
		f.t.Fatalf("the installer was never run: %v", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestServerReportsNoUpdateBeforeTheFirst(t *testing.T) {
	f := newUpdateFixture(t)

	if got := f.last(); got.State != agentproto.UpdateNone {
		t.Fatalf("status = %+v, want none", got)
	}
}

func TestServerRunsTheInstallerAsAUnitOfItsOwnAndRecordsHowItEnded(t *testing.T) {
	f := newUpdateFixture(t)

	started := decode[agentproto.UpdateStatus](t, f.post(""), http.StatusOK)
	if started.State != agentproto.UpdatePending || started.RunID == "" {
		t.Fatalf("answer = %+v, want a pending run with an id", started)
	}
	launched, err := os.ReadFile(f.runLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--no-block", "--collect", "x-ui-agent-update-" + started.RunID} {
		if !strings.Contains(string(launched), want) {
			t.Errorf("systemd-run was asked %q, want it to carry %q", launched, want)
		}
	}
	if got := f.installerArgs(); got != "" {
		t.Fatalf("installer arguments = %q, want none for the latest release", got)
	}

	done := f.last()
	if done.RunID != started.RunID || done.State != agentproto.UpdateSuccess || done.ExitCode != 0 || done.FinishedAt == 0 {
		t.Fatalf("status after the run = %+v, want it recorded as a success", done)
	}
	if left := f.scripts(); len(left) != 0 {
		t.Fatalf("installer copies left behind: %v", left)
	}
}

func TestServerMovesToTheDevChannelWhenAsked(t *testing.T) {
	f := newUpdateFixture(t)

	decode[agentproto.UpdateStatus](t, f.post("?"+agentproto.QueryUpdateDev+"=true"), http.StatusOK)
	if got := f.installerArgs(); got != "--version\ndev-latest" {
		t.Fatalf("installer arguments = %q, want --version dev-latest", got)
	}
}

func TestServerRecordsAnInstallerThatFailed(t *testing.T) {
	f := newUpdateFixture(t)
	t.Setenv("STAND_IN_EXIT", "3")

	decode[agentproto.UpdateStatus](t, f.post(""), http.StatusOK)
	if got := f.last(); got.State != agentproto.UpdateFailed || got.ExitCode != 3 {
		t.Fatalf("status = %+v, want a failure with the installer's exit code", got)
	}
	if left := f.scripts(); len(left) != 0 {
		t.Fatalf("installer copies left behind: %v", left)
	}
}

func TestServerStartsNoSecondUpdateWhileOneRuns(t *testing.T) {
	f := newUpdateFixture(t)
	running := agentproto.UpdateStatus{RunID: "1", State: agentproto.UpdatePending, StartedAt: time.Now().Add(-time.Minute).Unix()}
	if err := f.server.updater.write(running); err != nil {
		t.Fatal(err)
	}

	r := f.post("")
	if r.status != http.StatusConflict || !strings.Contains(r.body, "already running") {
		t.Fatalf("answer = %d %s, want a refusal", r.status, r.body)
	}
	if f.hits.Load() != 0 {
		t.Fatal("the installer was downloaded for an update that was refused")
	}
	if _, err := os.Stat(f.runLog); err == nil {
		t.Fatal("a second unit was started")
	}

	// A run that never wrote its result, long ago, does not block the next.
	running.StartedAt = time.Now().Add(-2 * updateStaleAfter).Unix()
	if err := f.server.updater.write(running); err != nil {
		t.Fatal(err)
	}
	decode[agentproto.UpdateStatus](t, f.post(""), http.StatusOK)
}

func TestServerRefusesToUpdateWhatTheInstallerDidNotInstall(t *testing.T) {
	f := newUpdateFixture(t)
	installedExecutable = "/usr/local/x-ui-agent/x-ui-agent"

	r := f.post("")
	if r.status != http.StatusConflict || !strings.Contains(r.body, "not installed by install-agent.sh") {
		t.Fatalf("answer = %d %s, want a refusal", r.status, r.body)
	}
	if f.hits.Load() != 0 {
		t.Fatal("the installer was downloaded for an agent that cannot use it")
	}
}

func TestServerRefusesToUpdateWithoutSystemd(t *testing.T) {
	f := newUpdateFixture(t)
	t.Setenv("PATH", t.TempDir())

	if r := f.post(""); r.status != http.StatusNotImplemented {
		t.Fatalf("answer = %d %s, want 501", r.status, r.body)
	}
}

func TestServerLeavesNothingBehindWhenTheUpdateCannotStart(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, f *updateFixture)
		want  string
	}{
		{"the download fails", func(_ *testing.T, f *updateFixture) { f.status.Store(http.StatusInternalServerError) }, "HTTP 500"},
		{"the answer is an error page", func(_ *testing.T, f *updateFixture) { f.script.Store("<html>blocked</html>") }, "not a script"},
		{"the answer is too large", func(_ *testing.T, f *updateFixture) {
			f.script.Store("#!/bin/sh\n" + strings.Repeat("#", maxInstallerBytes))
		}, "not a script"},
		{"systemd-run refuses", func(t *testing.T, f *updateFixture) { t.Setenv("FAKE_SYSTEMD_RUN_FAIL", "Failed to connect to bus") }, "Failed to connect to bus"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newUpdateFixture(t)
			tt.setup(t, f)

			r := f.post("")
			if r.status != http.StatusBadGateway || !strings.Contains(r.body, tt.want) {
				t.Fatalf("answer = %d %s, want a 502 that says %q", r.status, r.body, tt.want)
			}
			if got := f.last(); got.State != agentproto.UpdateNone {
				t.Fatalf("status = %+v, want no run recorded for an update that did not start", got)
			}
			if left := f.scripts(); len(left) != 0 {
				t.Fatalf("installer copies left behind: %v", left)
			}
		})
	}
}

func TestServerRefusesAnUpdateFlagThatIsNoBool(t *testing.T) {
	f := newUpdateFixture(t)

	if r := f.post("?" + agentproto.QueryUpdateDev + "=maybe"); r.status != http.StatusBadRequest {
		t.Fatalf("answer = %d %s, want 400", r.status, r.body)
	}
	if f.hits.Load() != 0 {
		t.Fatal("the installer was downloaded for a request that was refused")
	}
}

func TestServerKeepsTheUpdateRoutesBehindTheSecretAndTheirMethods(t *testing.T) {
	f := newUpdateFixture(t)

	for _, tt := range []struct{ method, authorization string }{
		{http.MethodGet, ""},
		{http.MethodPost, ""},
		{http.MethodPost, "Bearer wrong"},
		{http.MethodPut, "Bearer " + f.bundle.Secret},
		{http.MethodDelete, "Bearer " + f.bundle.Secret},
	} {
		if r := f.do(tt.method, agentproto.PathUpdate, nil, tt.authorization); r.status != http.StatusNotFound {
			t.Errorf("%s with %q: status = %d, want the bare 404", tt.method, tt.authorization, r.status)
		}
	}
	if f.hits.Load() != 0 {
		t.Fatal("a request the server refused started an update")
	}
}
