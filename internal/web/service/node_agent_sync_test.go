package service

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

type syncPush struct {
	restartFlag string
	body        []byte
}

// syncAgentFixture is a node row in the database that points at a stand-in for the agent's
// /v1 API, which the test tells how to answer and which keeps what it was sent.
type syncAgentFixture struct {
	t        *testing.T
	nodeID   int
	rt       *runtime.AgentRuntime
	nodes    NodeService
	mu       sync.Mutex
	pushes   []syncPush
	restarts int
	refuse   bool // answer pushes with 422
	broken   bool // answer pushes with 500
	geo      *syncGeoAgent
}

func newSyncAgentFixture(t *testing.T) *syncAgentFixture {
	t.Helper()
	setupSettingTestDB(t)
	f := &syncAgentFixture{t: t}
	bundle, fingerprint, err := agentproto.NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := bundle.ServerTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
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
	f.nodeID = node.Id
	f.rt = runtime.NewAgentRuntime(f.node(), nil)
	return f
}

func (f *syncAgentFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "PUT " + agentproto.PathConfig:
		body, _ := io.ReadAll(r.Body)
		flag := r.URL.Query().Get(agentproto.QueryRestartOnUserRemoval)
		f.pushes = append(f.pushes, syncPush{restartFlag: flag, body: body})
		switch {
		case f.refuse:
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error":"the core rejected the config"}`))
		case f.broken:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"boom"}`))
		default:
			restart, _ := strconv.ParseBool(flag)
			revision := agentproto.RevisionOf(body, restart)
			_, _ = w.Write([]byte(`{"revision":"` + revision + `","applied":"restart","xrayState":"running"}`))
		}
	case "POST " + agentproto.PathRestart:
		f.restarts++
		_, _ = w.Write([]byte(`{"revision":"x","applied":"restart","xrayState":"running"}`))
	default:
		if !f.serveGeo(w, r) {
			http.NotFound(w, r)
		}
	}
}

func (f *syncAgentFixture) node() *model.Node {
	f.t.Helper()
	n, err := f.nodes.GetById(f.nodeID)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *syncAgentFixture) pushed() []syncPush {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]syncPush(nil), f.pushes...)
}

func (f *syncAgentFixture) sync(svc *AgentSyncService, stats *agentproto.Stats) error {
	f.t.Helper()
	return svc.Sync(context.Background(), f.rt, f.node(), stats)
}

func (f *syncAgentFixture) markDirty() {
	f.t.Helper()
	if err := f.nodes.MarkNodeDirty(f.nodeID); err != nil {
		f.t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
}

func (f *syncAgentFixture) wantRendered() []byte {
	f.t.Helper()
	body, err := (&XrayService{}).RenderAgentConfig(f.nodeID)
	if err != nil {
		f.t.Fatal(err)
	}
	return body
}

func TestAgentSyncPushesADirtyNodeAndClearsTheMark(t *testing.T) {
	f := newSyncAgentFixture(t)
	seedInboundOn(t, &f.nodeID, "n1-vless", model.VLESS, true, []model.Client{
		{Email: "a@x", ID: "11111111-1111-1111-1111-111111111111", Enable: true},
	})
	f.markDirty()

	svc := &AgentSyncService{}
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	pushes := f.pushed()
	if len(pushes) != 1 || string(pushes[0].body) != string(f.wantRendered()) || pushes[0].restartFlag != "true" {
		t.Fatalf("pushes = %+v, want one carrying the rendered config with the default restart policy (on)", pushes)
	}
	if f.node().ConfigDirty {
		t.Fatal("the node is still dirty after the agent took the config")
	}
}

func TestAgentSyncSendsTheRestartPolicyOfTheSetting(t *testing.T) {
	f := newSyncAgentFixture(t)
	if err := (&SettingService{}).SetRestartXrayOnClientDisable(false); err != nil {
		t.Fatal(err)
	}
	f.markDirty()

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	pushes := f.pushed()
	if len(pushes) != 1 || pushes[0].restartFlag != "false" {
		t.Fatalf("pushes = %+v, want one with restartOnUserRemoval=false", pushes)
	}
}

func TestAgentSyncDoesNotPushWhatTheAgentAlreadyRuns(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.markDirty()
	stats := &agentproto.Stats{ConfigRevision: agentproto.RevisionOf(f.wantRendered(), true)}

	if err := f.sync(&AgentSyncService{}, stats); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 0 {
		t.Fatalf("%d pushes to an agent that already runs the rendered config", got)
	}
	if f.node().ConfigDirty {
		t.Fatal("the node stays dirty although the agent runs what the master renders")
	}
}

func TestAgentSyncRepairsDriftOnceAndThenWaitsForTheNextCheck(t *testing.T) {
	f := newSyncAgentFixture(t)
	svc := &AgentSyncService{}
	stale := &agentproto.Stats{ConfigRevision: "left-over-from-before-a-reinstall"}

	if err := f.sync(svc, stale); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes on the first look at a clean node running another config, want 1", got)
	}
	if err := f.sync(svc, stale); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes after a second look inside the check interval, want still 1", got)
	}

	svc.agents[f.nodeID].checkedAt = time.Now().Add(-agentDriftCheckEvery)
	if err := f.sync(svc, stale); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 2 {
		t.Fatalf("%d pushes once the interval passed, want the drift found again", got)
	}
}

func TestAgentSyncLeavesARefusedConfigAloneUntilItChanges(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.refuse = true
	svc := &AgentSyncService{}
	f.markDirty()

	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("a refusal is the agent's answer, not a failure to reach it: %v", err)
	}
	if !f.node().ConfigDirty {
		t.Fatal("the node was cleared although the agent refused its config")
	}
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes, want the refused config tried once", got)
	}

	st := svc.agents[f.nodeID]
	st.checkedAt = time.Now().Add(-agentDriftCheckEvery)
	f.markDirty()
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes after another mutation that left the render as it was, want the refusal still honoured", got)
	}

	seedInboundOn(t, &f.nodeID, "n1-fix", model.VLESS, true, nil)
	f.markDirty()
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.pushed()); got != 2 {
		t.Fatalf("%d pushes after the config changed, want it tried at once", got)
	}

	st.refusedAt = time.Now().Add(-agentRefusalRetryAfter)
	st.checkedAt = time.Now().Add(-agentDriftCheckEvery)
	f.markDirty()
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.pushed()); got != 3 {
		t.Fatalf("%d pushes once the refusal is old, want one more try", got)
	}
}

// A failure that keeps coming back must not turn into a render and a push on every tick.
func TestAgentSyncWaitsBeforeTryingAFailedPushAgain(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.broken = true
	svc := &AgentSyncService{}
	f.markDirty()

	if err := f.sync(svc, nil); err == nil {
		t.Fatal("a push the agent answered with a 500 was reported as done")
	}
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("the next tick tried again at once: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes over two ticks, want the failed one only", got)
	}

	f.markDirty()
	if err := f.sync(svc, nil); err == nil {
		t.Fatal("the push after a mutation also failed, want that reported")
	}
	if got := len(f.pushed()); got != 2 {
		t.Fatalf("%d pushes, want a mutation to ask for one at once", got)
	}

	f.mu.Lock()
	f.broken = false
	f.mu.Unlock()
	svc.agents[f.nodeID].checkedAt = time.Now().Add(-agentDriftCheckEvery)
	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync once the wait is over: %v", err)
	}
	if got := len(f.pushed()); got != 3 || f.node().ConfigDirty {
		t.Fatalf("%d pushes, dirty %v; want the node cleared by the third", got, f.node().ConfigDirty)
	}
}

// The push that drops the user carries the restart policy, so a blind restart into the
// config the agent still holds would only add a second one.
func TestRestartOnDisableLeavesAnAgentToItsPush(t *testing.T) {
	f := newSyncAgentFixture(t)
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}})
	prev := runtime.GetManager()
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(prev) })
	mgr.SetRuntimeOverride(f.nodeID, f.rt)
	panel := &restartRecorder{}
	mgr.SetRuntimeOverride(f.nodeID+1, panel)

	(&InboundService{}).restartRemoteNodesOnDisable([]int{f.nodeID, f.nodeID + 1})

	for deadline := time.Now().Add(5 * time.Second); panel.calls.Load() == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the panel node was never asked to restart")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.restarts != 0 {
		t.Fatalf("the agent was asked to restart %d times, want it left to the next push", f.restarts)
	}
}

type restartRecorder struct {
	runtime.Runtime
	calls atomic.Int32
}

func (r *restartRecorder) RestartXray(context.Context) error {
	r.calls.Add(1)
	return nil
}
