package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// syncSidecarsAgent is the helper-programs endpoint of the stand-in agent, which the fixture answers with
// a 404 until a test sets one, as an agent that predates it would.
type syncSidecarsAgent struct {
	puts    [][]string // the list of each PUT
	status  int        // answer every PUT with this status and message instead of taking the list
	message string
}

// serveSidecars answers the helper-programs endpoint, with f.mu held; false means the request is not for it.
func (f *syncAgentFixture) serveSidecars(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.PathSidecars || r.Method != http.MethodPut || f.sidecars == nil {
		return false
	}
	var spec agentproto.SidecarsSpec
	_ = json.NewDecoder(r.Body).Decode(&spec)
	f.sidecars.puts = append(f.sidecars.puts, spec.Enabled)
	if f.sidecars.status != 0 {
		w.WriteHeader(f.sidecars.status)
		if f.sidecars.message != "" {
			_ = json.NewEncoder(w).Encode(agentproto.ErrorBody{Error: f.sidecars.message})
		}
		return true
	}
	_ = json.NewEncoder(w).Encode(agentproto.Sidecars{Revision: agentproto.SidecarsRevision(spec.Enabled), Enabled: spec.Enabled})
	return true
}

func (f *syncAgentFixture) sidecarPuts() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.sidecars.puts...)
}

func (f *syncAgentFixture) wantSidecars(names ...string) {
	f.t.Helper()
	list, err := json.Marshal(names)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", f.nodeID).Update("sidecars", string(list)).Error; err != nil {
		f.t.Fatal(err)
	}
}

// retryNow lets the next tick send the helper programs again, as a tick after the wait would.
func (s *AgentSyncService) retryNow(nodeID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateFor(nodeID).sidecarsRetry = time.Time{}
}

func torOnly() []string { return []string{agentproto.SidecarTor} }

func TestAgentSyncSendsTheHelperProgramsTheAgentDoesNotRun(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{}
	t.Cleanup(func() { setAgentSidecarProblem(f.nodeID, "") })
	f.wantSidecars(agentproto.SidecarTor)

	// An agent that predates the list reports no revision, which is the one of an empty list.
	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.sidecarPuts(); len(puts) != 1 || !slices.Equal(puts[0], torOnly()) {
		t.Fatalf("PUTs = %q, want one carrying tor", puts)
	}
	if problem := agentSidecarProblem(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the agent took the list", problem)
	}
}

func TestAgentSyncLeavesTheHelperProgramsAnAgentAlreadyHolds(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{}
	svc := &AgentSyncService{}

	// Neither wants any: an agent that predates the list is left alone, not asked to take an empty one.
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync with none wanted: %v", err)
	}
	f.wantSidecars(agentproto.SidecarTor)
	held := &agentproto.Stats{SidecarsRevision: agentproto.SidecarsRevision(torOnly())}
	if err := f.sync(svc, held); err != nil {
		t.Fatalf("Sync with tor held: %v", err)
	}
	if puts := f.sidecarPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %q, want none while the agent holds what the node asks for", puts)
	}
}

func TestAgentSyncStopsTheHelperProgramsTheNodeNoLongerAsksFor(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{}

	held := &agentproto.Stats{SidecarsRevision: agentproto.SidecarsRevision(torOnly())}
	if err := f.sync(&AgentSyncService{}, held); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	puts := f.sidecarPuts()
	if len(puts) != 1 || puts[0] == nil || len(puts[0]) != 0 {
		t.Fatalf("PUTs = %#v, want one that carries an empty list, which stops what runs", puts)
	}
}

func TestAgentSyncWaitsForStatsBeforeTouchingTheHelperPrograms(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{}
	f.wantSidecars(agentproto.SidecarTor)

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.sidecarPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %q without knowing what the agent holds", puts)
	}
}

func TestAgentSyncSaysAnAgentIsTooOldForHelperPrograms(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{status: http.StatusNotFound}
	t.Cleanup(func() { setAgentSidecarProblem(f.nodeID, "") })
	f.wantSidecars(agentproto.SidecarTor)
	svc := &AgentSyncService{}

	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if problem := agentProblems(f.nodeID); problem != agentTooOldForSidecars {
		t.Fatalf("problem = %q, want %q", problem, agentTooOldForSidecars)
	}
	// Asking an agent that cannot answer again every tick would fill its log with refusals.
	for range 3 {
		if err := f.sync(svc, &agentproto.Stats{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if puts := f.sidecarPuts(); len(puts) != 1 {
		t.Fatalf("%d PUTs to an agent that does not know the path, want one", len(puts))
	}

	// The agent is updated: it takes the list and the problem goes.
	f.sidecars.status = 0
	svc.retryNow(f.nodeID)
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync after the update: %v", err)
	}
	if problem := agentProblems(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the agent took the list", problem)
	}
}

func TestAgentSyncShowsWhyAnAgentRefusedTheHelperPrograms(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{status: http.StatusBadRequest, message: `unknown helper program "tor"`}
	t.Cleanup(func() { setAgentSidecarProblem(f.nodeID, "") })
	f.wantSidecars(agentproto.SidecarTor)

	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if problem := agentSidecarProblem(f.nodeID); !strings.Contains(problem, `unknown helper program "tor"`) {
		t.Fatalf("problem = %q, want the reason the agent gave", problem)
	}
}

func TestAgentSyncRetriesTheHelperProgramsAfterAFailure(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{status: http.StatusInternalServerError, message: "boom"}
	t.Cleanup(func() { setAgentSidecarProblem(f.nodeID, "") })
	f.wantSidecars(agentproto.SidecarTor)
	svc := &AgentSyncService{}

	err := f.sync(svc, &agentproto.Stats{})
	if err == nil || !strings.Contains(err.Error(), "helper programs") {
		t.Fatalf("Sync error = %v, want a failed set of the helper programs", err)
	}
	if problem := agentSidecarProblem(f.nodeID); problem != "" {
		t.Fatalf("problem = %q for a failure that may pass, want none", problem)
	}
	if err := f.sync(svc, &agentproto.Stats{}); err != nil || len(f.sidecarPuts()) != 1 {
		t.Fatalf("the next tick sent %d PUTs (%v), want it to wait out the retry delay", len(f.sidecarPuts()), err)
	}

	f.sidecars.status = 0
	svc.retryNow(f.nodeID)
	if err := f.sync(svc, &agentproto.Stats{}); err != nil || len(f.sidecarPuts()) != 2 {
		t.Fatalf("after the delay: %d PUTs, %v; want the list sent again", len(f.sidecarPuts()), err)
	}
}

func TestAgentProblemsShowTheHelperProgramErrorBesideTheConfigOne(t *testing.T) {
	const nodeID = 90001
	setAgentConfigProblem(nodeID, "the config of this node was adjusted: x")
	setAgentSidecarProblem(nodeID, agentTooOldForSidecars)
	t.Cleanup(func() { setAgentConfigProblem(nodeID, ""); setAgentSidecarProblem(nodeID, "") })

	got := agentProblems(nodeID)
	if !strings.Contains(got, "adjusted: x") || !strings.Contains(got, agentTooOldForSidecars) {
		t.Fatalf("agentProblems = %q, want both the config's and the helper programs'", got)
	}
	setAgentConfigProblem(nodeID, "")
	if got := agentProblems(nodeID); got != agentTooOldForSidecars {
		t.Fatalf("agentProblems = %q, want the helper programs' alone", got)
	}
	setAgentSidecarProblem(nodeID, "")
	if got := agentProblems(nodeID); got != "" {
		t.Fatalf("agentProblems = %q, want none", got)
	}
}

func TestAgentSyncRefusesAStoredHelperProgramItDoesNotKnow(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.sidecars = &syncSidecarsAgent{}
	t.Cleanup(func() { setAgentSidecarProblem(f.nodeID, "") })
	f.wantSidecars("tor", "gone")

	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	var unknown *agentproto.UnknownSidecarError
	if _, err := agentproto.NormalizeSidecars([]string{"gone"}); !errors.As(err, &unknown) {
		t.Fatal("the test names a program that is known")
	}
	if puts := f.sidecarPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %q, want none for a list the panel cannot make sense of", puts)
	}
	if problem := agentSidecarProblem(f.nodeID); !strings.Contains(problem, `"gone"`) {
		t.Fatalf("problem = %q, want the unknown program named", problem)
	}
}

// The master and the agent are two programs that agree on a wire form, so this runs the real agent server
// against the real client and sync rather than a stand-in.
func TestHelperProgramsGoToARealAgentAndComeBackInItsReports(t *testing.T) {
	node, _ := startIdleAgent(t)
	node.Sidecars = torOnly()
	t.Cleanup(func() { setAgentSidecarProblem(node.Id, "") })
	rt := runtime.NewAgentRuntime(node, nil)
	client, err := rt.Client()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &AgentSyncService{}

	stats, err := client.Stats(ctx)
	if err != nil || stats.SidecarsRevision != "" {
		t.Fatalf("stats = %+v, %v; want an agent that runs nothing yet", stats, err)
	}
	if err := svc.syncSidecars(ctx, rt, node, stats, time.Now()); err != nil {
		t.Fatalf("syncSidecars: %v", err)
	}

	stats, err = client.Stats(ctx)
	if err != nil || stats.SidecarsRevision != agentproto.SidecarsRevision(torOnly()) {
		t.Fatalf("stats = %+v, %v; want the agent to report the revision of the list it took", stats, err)
	}
	patch, err := (&NodeService{}).Probe(ctx, node)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if want := []model.NodeSidecar{{Name: "tor", State: agentproto.SidecarPending}}; !slices.Equal(patch.SidecarStates, want) {
		t.Fatalf("states = %+v, want %+v: the agent was not running its loop, so it has not looked at tor", patch.SidecarStates, want)
	}

	// With the list held, the next tick has nothing to send, and an emptied list is taken the same way.
	if err := svc.syncSidecars(ctx, rt, node, stats, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("syncSidecars with the list held: %v", err)
	}
	node.Sidecars = nil
	if err := svc.syncSidecars(ctx, rt, node, stats, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("syncSidecars with the list emptied: %v", err)
	}
	if stats, err = client.Stats(ctx); err != nil || stats.SidecarsRevision != "" {
		t.Fatalf("stats = %+v, %v; want the agent to hold no list once the node asks for none", stats, err)
	}
}
