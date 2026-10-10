package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	updateStatusFile  = "update.json"
	maxInstallerBytes = 1 << 20
	// The installer is a few kilobytes, and the master that waits for the answer to start an update
	// gives up after 20 s, so downloading it and queueing the unit must fit well inside that.
	installerFetchTimeout = 10 * time.Second
	systemdRunTimeout     = 5 * time.Second
	// A run that never wrote its result (the host went down under it) stops blocking the next.
	updateStaleAfter = 30 * time.Minute

	// The wrapper runs the installer as a transient unit of its own, since the installer stops
	// this agent. $1 is the installer, $2 the status file, $3 the run, the rest its arguments.
	updateWrapper = `script=$1 status=$2 run=$3
shift 3
/bin/bash "$script" "$@"
code=$?
rm -f "$script"
if [ "$code" -eq 0 ]; then state=success; else state=failed; fi
printf '{"runId":"%s","state":"%s","exitCode":%d,"finishedAt":%d}\n' "$run" "$state" "$code" "$(date +%s)" > "$status.tmp" && mv -f "$status.tmp" "$status"
exit "$code"`
)

// Variables so tests can point them elsewhere. The installer is the one the operator ran, from the
// same place: the agent does not take the address from the master.
var (
	installerURL        = "https://raw.githubusercontent.com/kuzzrus/3x-ui-awg/main/install-agent.sh"
	installedExecutable = "/usr/local/x-ui-agent/x-ui-agent"
)

var (
	errUpdateRunning = errors.New("an update is already running")
	errNotInstalled  = errors.New("this agent was not installed by install-agent.sh, so it cannot update itself")
	errNoSystemd     = errors.New("self-update needs systemd-run, which this host does not have")
)

// updater updates the agent by running its installer again, which keeps the pairing bundle,
// checks the download against its checksum and swaps the binary and the core.
type updater struct {
	stateDir string
	mu       sync.Mutex
}

func (u *updater) statusPath() string { return filepath.Join(u.stateDir, updateStatusFile) }

// read is the last run, or none when there was none or its record is unreadable.
func (u *updater) read() agentproto.UpdateStatus {
	raw, err := os.ReadFile(u.statusPath())
	if err != nil {
		return agentproto.UpdateStatus{State: agentproto.UpdateNone}
	}
	var status agentproto.UpdateStatus
	if json.Unmarshal(raw, &status) != nil || status.RunID == "" {
		return agentproto.UpdateStatus{State: agentproto.UpdateNone}
	}
	return status
}

// start launches an update and returns at once: the installer stops this process before it ends.
func (u *updater) start(ctx context.Context, dev bool) (agentproto.UpdateStatus, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if err := checkInstalled(); err != nil {
		return agentproto.UpdateStatus{}, err
	}
	systemdRun, err := exec.LookPath("systemd-run")
	if err != nil {
		return agentproto.UpdateStatus{}, errNoSystemd
	}
	if last := u.read(); last.State == agentproto.UpdatePending && time.Since(time.Unix(last.StartedAt, 0)) < updateStaleAfter {
		return last, errUpdateRunning
	}

	runID := strconv.FormatInt(time.Now().UnixNano(), 10)
	script, err := u.fetchInstaller(ctx, runID)
	if err != nil {
		return agentproto.UpdateStatus{}, err
	}
	status := agentproto.UpdateStatus{RunID: runID, State: agentproto.UpdatePending, StartedAt: time.Now().Unix()}
	if err := u.write(status); err != nil {
		_ = os.Remove(script)
		return agentproto.UpdateStatus{}, err
	}

	args := []string{
		"--quiet", "--no-block", "--collect", "--unit", "x-ui-agent-update-" + runID,
		"/bin/bash", "-c", updateWrapper, "x-ui-agent-update", script, u.statusPath(), runID,
	}
	if dev {
		args = append(args, "--version", "dev-latest")
	}
	// The master hanging up on its own deadline must not kill systemd-run once the unit may be queued.
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), systemdRunTimeout)
	defer cancel()
	if out, err := exec.CommandContext(runCtx, systemdRun, args...).CombinedOutput(); err != nil {
		_ = os.Remove(script)
		_ = os.Remove(u.statusPath())
		return agentproto.UpdateStatus{}, fmt.Errorf("start the update job: %w: %s", err, strings.TrimSpace(string(out)))
	}
	logger.Infof("agent: update %s started (dev channel: %v)", runID, dev)
	return status, nil
}

func (u *updater) write(status agentproto.UpdateStatus) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return xray.WriteFileAtomic(u.statusPath(), append(raw, '\n'), stateMode)
}

// fetchInstaller downloads the installer into the state folder, private to root.
func (u *updater) fetchInstaller(ctx context.Context, runID string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, installerFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, installerURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download the installer: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download the installer: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxInstallerBytes+1))
	if err != nil {
		return "", fmt.Errorf("download the installer: %w", err)
	}
	// A proxy's error page is no script, and a script that large is no installer.
	if len(body) > maxInstallerBytes || !strings.HasPrefix(string(body), "#!") {
		return "", errors.New("download the installer: the answer is not a script")
	}
	path := filepath.Join(u.stateDir, "update-"+runID+".sh")
	if err := os.WriteFile(path, body, 0o700); err != nil {
		return "", err
	}
	return path, nil
}

func checkInstalled() error {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil || exe != installedExecutable {
		return errNotInstalled
	}
	return nil
}

func (s *Server) postUpdate(w http.ResponseWriter, r *http.Request) {
	dev := false
	if value := r.URL.Query().Get(agentproto.QueryUpdateDev); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be true or false", agentproto.QueryUpdateDev))
			return
		}
		dev = parsed
	}
	status, err := s.updater.start(r.Context(), dev)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, status)
	case errors.Is(err, errUpdateRunning), errors.Is(err, errNotInstalled):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errNoSystemd):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (s *Server) getUpdate(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.updater.read())
}
