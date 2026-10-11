package service

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// opsAgent is a stand-in for the agent's log and restart endpoints, and the node row that points at it.
type opsAgent struct {
	nodeID int
	mu     sync.Mutex
	// What it answers, and what it was asked.
	logs        []string
	logsStatus  int    // 0 is a 200
	restartFail string // answers the restart with a 409 and this reason
	logQueries  []string
	restarts    int
}

func newOpsAgent(t *testing.T) *opsAgent {
	t.Helper()
	setupSettingTestDB(t)
	a := &opsAgent{}
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := bundle.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(a.serve))
	srv.TLS = tlsConfig
	srv.StartTLS()
	t.Cleanup(srv.Close)

	node := &model.Node{
		Name: "agent", Kind: model.NodeKindAgent, Address: "127.0.0.1",
		Port:     srv.Listener.Addr().(*net.TCPAddr).Port,
		ApiToken: bundle.Secret, PinnedCertSha256: fingerprint, AllowPrivateAddress: true, Enable: true,
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	a.nodeID = node.Id

	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}})
	prev := runtime.GetManager()
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(prev) })
	return a
}

func (a *opsAgent) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET " + agentproto.PathLogs:
		a.logQueries = append(a.logQueries, r.URL.RawQuery)
		if a.logsStatus != 0 {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(agentproto.Logs{Lines: a.logs})
	case "POST " + agentproto.PathRestart:
		if a.restartFail != "" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(agentproto.ErrorBody{Error: a.restartFail})
			return
		}
		a.restarts++
		_ = json.NewEncoder(w).Encode(agentproto.ConfigResponse{Revision: "r", Applied: agentproto.AppliedRestart, XrayState: agentproto.XrayStateRunning})
	default:
		http.NotFound(w, r)
	}
}

func (a *opsAgent) restartCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.restarts
}

func TestAgentLogsReadsTheNewestLinesAtTheLevelAsked(t *testing.T) {
	a := newOpsAgent(t)
	a.logs = []string{"2026/10/10 12:00:02 ERROR - second", "2026/10/10 12:00:01 INFO - XRAY: first"}

	got, err := (&NodeService{}).AgentLogs(a.nodeID, 50, "warning")
	if err != nil || len(got) != 2 || !strings.Contains(got[0], "second") {
		t.Fatalf("AgentLogs = %v, %v, want the lines as the agent sent them", got, err)
	}
	if len(a.logQueries) != 1 || a.logQueries[0] != "count=50&level=warning" {
		t.Fatalf("the agent was asked %v, want the count and the level in the query", a.logQueries)
	}
}

func TestAgentLogsRefusesWhatIsNoAgent(t *testing.T) {
	setupSettingTestDB(t)
	node := &model.Node{Name: "panel", Scheme: "https", Address: "192.0.2.9", Port: 2053, BasePath: "/", Enable: true}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	prev := runtime.GetManager()
	runtime.SetManager(runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}}))
	t.Cleanup(func() { runtime.SetManager(prev) })

	if _, err := (&NodeService{}).AgentLogs(node.Id, 20, "info"); err == nil || !strings.Contains(err.Error(), "only an agent node") {
		t.Fatalf("error = %v, want a panel node refused", err)
	}
	if _, err := (&NodeService{}).AgentLogs(node.Id+99, 20, "info"); err == nil {
		t.Fatal("a node that does not exist was read")
	}
}

func TestAgentLogsSaysWhenTheAgentIsTooOldToServeThem(t *testing.T) {
	a := newOpsAgent(t)
	a.logsStatus = http.StatusNotFound

	if _, err := (&NodeService{}).AgentLogs(a.nodeID, 20, "info"); err == nil || !strings.Contains(err.Error(), "too old to serve its log") {
		t.Fatalf("error = %v, want the agent said to be too old, not a rejected secret", err)
	}
}

func TestRestartNodeXrayRestartsTheCoreOfAnAgent(t *testing.T) {
	a := newOpsAgent(t)

	if err := (&NodeService{}).RestartNodeXray(a.nodeID); err != nil {
		t.Fatalf("RestartNodeXray: %v", err)
	}
	if got := a.restartCount(); got != 1 {
		t.Fatalf("the agent was asked to restart %d times, want once", got)
	}
}

func TestRestartNodeXrayReportsWhyTheAgentWillNot(t *testing.T) {
	a := newOpsAgent(t)
	a.restartFail = "no config has been applied yet"

	if err := (&NodeService{}).RestartNodeXray(a.nodeID); err == nil || !strings.Contains(err.Error(), "no config has been applied yet") {
		t.Fatalf("error = %v, want the agent's own reason", err)
	}
}

func TestRestartNodeXrayLeavesANodeThatIsSwitchedOff(t *testing.T) {
	a := newOpsAgent(t)
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", a.nodeID).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}

	if err := (&NodeService{}).RestartNodeXray(a.nodeID); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error = %v, want the disabled node refused", err)
	}
	if got := a.restartCount(); got != 0 {
		t.Fatalf("a disabled node was asked to restart %d times", got)
	}
}

func TestRestartNodeXrayRestartsAPanelNodeThroughItsRuntime(t *testing.T) {
	setupSettingTestDB(t)
	node := &model.Node{Name: "panel", Scheme: "https", Address: "192.0.2.9", Port: 2053, BasePath: "/", Enable: true}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}})
	panel := &restartRecorder{}
	mgr.SetRuntimeOverride(node.Id, panel)
	prev := runtime.GetManager()
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(prev) })

	if err := (&NodeService{}).RestartNodeXray(node.Id); err != nil {
		t.Fatalf("RestartNodeXray: %v", err)
	}
	if got := panel.calls.Load(); got != 1 {
		t.Fatalf("the panel node was asked to restart %d times, want once", got)
	}
}
