package service

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

// syncDecoyAgent is the decoy endpoint of the stand-in agent, which the fixture answers with a 404 until a
// test sets one, as an agent that predates it would.
type syncDecoyAgent struct {
	puts    []agentproto.DecoySpec
	status  int // answer every PUT with this status and message instead of taking the decoy
	message string
}

// serveDecoy answers the decoy endpoint, with f.mu held; false means the request is not for it.
func (f *syncAgentFixture) serveDecoy(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.PathDecoy || r.Method != http.MethodPut || f.decoy == nil {
		return false
	}
	var spec agentproto.DecoySpec
	_ = json.NewDecoder(r.Body).Decode(&spec)
	f.decoy.puts = append(f.decoy.puts, spec)
	if f.decoy.status != 0 {
		w.WriteHeader(f.decoy.status)
		if f.decoy.message != "" {
			_ = json.NewEncoder(w).Encode(agentproto.ErrorBody{Error: f.decoy.message})
		}
		return true
	}
	_ = json.NewEncoder(w).Encode(agentproto.Decoy{Revision: agentproto.DecoyRevision(spec), Spec: spec})
	return true
}

func (f *syncAgentFixture) decoyPuts() []agentproto.DecoySpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentproto.DecoySpec(nil), f.decoy.puts...)
}

func (f *syncAgentFixture) wantDecoy(decoy model.NodeDecoy) {
	f.t.Helper()
	raw, err := json.Marshal(decoy)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", f.nodeID).Update("decoy", string(raw)).Error; err != nil {
		f.t.Fatal(err)
	}
}

// retryDecoyNow lets the next tick send the decoy again, as a tick after the wait would.
func (s *AgentSyncService) retryDecoyNow(nodeID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateFor(nodeID).decoyRetry = time.Time{}
}

var servedDecoy = model.NodeDecoy{Mode: "template", Template: "parked", Domain: "node.example", Cert: "auto", Port: 7443}

func servedSpec(t *testing.T) agentproto.DecoySpec {
	t.Helper()
	want, err := normalizeNodeDecoy(servedDecoy)
	if err != nil {
		t.Fatal(err)
	}
	return decoySpecOf(want)
}

func TestAgentSyncSendsTheDecoyTheAgentDoesNotServe(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })
	f.wantDecoy(servedDecoy)

	// An agent that predates the decoy reports no revision, which is the one of no decoy.
	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 1 || puts[0] != servedSpec(t) {
		t.Fatalf("PUTs = %+v, want one carrying the decoy as the agent stores it", puts)
	}
	if problem := agentDecoyProblems.get(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the agent took the decoy", problem)
	}
}

func TestAgentSyncLeavesTheDecoyAnAgentAlreadyServes(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}
	svc := &AgentSyncService{}

	// Neither wants one: an agent that predates the decoy is left alone, not asked to take none.
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync with none wanted: %v", err)
	}
	f.wantDecoy(servedDecoy)
	held := &agentproto.Stats{DecoyRevision: agentproto.DecoyRevision(servedSpec(t))}
	if err := f.sync(svc, held); err != nil {
		t.Fatalf("Sync with the decoy held: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %+v, want none while the agent serves what the node asks for", puts)
	}
}

func TestAgentSyncTakesTheDecoyOffAnAgentWhenTheNodeNoLongerAsksForIt(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}

	held := &agentproto.Stats{DecoyRevision: agentproto.DecoyRevision(servedSpec(t))}
	if err := f.sync(&AgentSyncService{}, held); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 1 || puts[0] != (agentproto.DecoySpec{}) {
		t.Fatalf("PUTs = %+v, want one that carries no decoy, which stops what is served", puts)
	}
}

func TestAgentSyncResendsADecoyThatChanged(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}
	other := servedDecoy
	other.Template = "tetris"
	f.wantDecoy(other)

	stale := &agentproto.Stats{DecoyRevision: agentproto.DecoyRevision(servedSpec(t))}
	if err := f.sync(&AgentSyncService{}, stale); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 1 || puts[0].Template != "tetris" {
		t.Fatalf("PUTs = %+v, want the new page sent to the agent that serves the old one", puts)
	}
}

func TestAgentSyncWaitsForStatsBeforeTouchingTheDecoy(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}
	f.wantDecoy(servedDecoy)

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %+v without knowing what the agent serves", puts)
	}
}

func TestAgentSyncSaysAnAgentIsTooOldToServeADecoy(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{status: http.StatusNotFound}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })
	f.wantDecoy(servedDecoy)
	svc := &AgentSyncService{}

	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if problem := agentProblems(f.nodeID); problem != agentTooOldForDecoy {
		t.Fatalf("problem = %q, want %q", problem, agentTooOldForDecoy)
	}
	for range 3 {
		if err := f.sync(svc, &agentproto.Stats{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	if puts := f.decoyPuts(); len(puts) != 1 {
		t.Fatalf("%d PUTs to an agent that does not know the path, want one", len(puts))
	}

	f.decoy.status = 0
	svc.retryDecoyNow(f.nodeID)
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync after the update: %v", err)
	}
	if problem := agentProblems(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the agent took the decoy", problem)
	}
}

func TestAgentSyncShowsWhyAnAgentRefusedTheDecoy(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{status: http.StatusBadRequest, message: `unknown decoy page "parked"`}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })
	f.wantDecoy(servedDecoy)

	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if problem := agentDecoyProblems.get(f.nodeID); !strings.Contains(problem, "refused the decoy") || !strings.Contains(problem, `unknown decoy page "parked"`) {
		t.Fatalf("problem = %q, want the reason the agent gave", problem)
	}
}

func TestAgentSyncRetriesTheDecoyAfterAFailure(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{status: http.StatusInternalServerError, message: "boom"}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })
	f.wantDecoy(servedDecoy)
	svc := &AgentSyncService{}

	err := f.sync(svc, &agentproto.Stats{})
	if err == nil || !strings.Contains(err.Error(), "decoy") {
		t.Fatalf("Sync error = %v, want a failed set of the decoy", err)
	}
	if err := f.sync(svc, &agentproto.Stats{}); err != nil || len(f.decoyPuts()) != 1 {
		t.Fatalf("the next tick sent %d PUTs (%v), want it to wait out the retry delay", len(f.decoyPuts()), err)
	}
	f.decoy.status = 0
	svc.retryDecoyNow(f.nodeID)
	if err := f.sync(svc, &agentproto.Stats{}); err != nil || len(f.decoyPuts()) != 2 {
		t.Fatalf("after the delay: %d PUTs, %v; want the decoy sent again", len(f.decoyPuts()), err)
	}
}

func TestAgentSyncRefusesAStoredDecoyThePanelCannotServe(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.decoy = &syncDecoyAgent{}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })
	f.wantDecoy(model.NodeDecoy{Mode: "template", Template: "gone-from-this-build", Domain: "node.example"})

	if err := f.sync(&AgentSyncService{}, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if puts := f.decoyPuts(); len(puts) != 0 {
		t.Fatalf("PUTs = %+v, want none for a decoy the panel cannot make sense of", puts)
	}
	if problem := agentDecoyProblems.get(f.nodeID); !strings.Contains(problem, "gone-from-this-build") {
		t.Fatalf("problem = %q, want the page named", problem)
	}
}

func TestAgentProblemsShowTheDecoyErrorBesideTheOthers(t *testing.T) {
	const nodeID = 90002
	setAgentConfigProblem(nodeID, "the config of this node was adjusted: x")
	setAgentSidecarProblem(nodeID, agentTooOldForSidecars)
	setAgentDecoyProblem(nodeID, agentTooOldForDecoy)
	t.Cleanup(func() {
		setAgentConfigProblem(nodeID, "")
		setAgentSidecarProblem(nodeID, "")
		setAgentDecoyProblem(nodeID, "")
	})

	got := agentProblems(nodeID)
	for _, want := range []string{"adjusted: x", agentTooOldForSidecars, agentTooOldForDecoy} {
		if !strings.Contains(got, want) {
			t.Fatalf("agentProblems = %q, want it to hold %q", got, want)
		}
	}
	setAgentDecoyProblem(nodeID, "")
	if got := agentProblems(nodeID); strings.Contains(got, agentTooOldForDecoy) {
		t.Fatalf("agentProblems = %q, want the decoy's gone once it is", got)
	}
}

// The master and the agent are two programs that agree on a wire form, so this runs the real agent server
// against the real client and sync rather than a stand-in.
func TestADecoyGoesToARealAgentAndComesBackInItsReports(t *testing.T) {
	node, _ := startIdleAgent(t)
	node.Decoy = servedDecoy
	t.Cleanup(func() { setAgentDecoyProblem(node.Id, "") })
	rt := runtime.NewAgentRuntime(node, nil)
	client, err := rt.Client()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	svc := &AgentSyncService{}

	stats, err := client.Stats(ctx)
	if err != nil || stats.DecoyRevision != "" {
		t.Fatalf("stats = %+v, %v; want an agent that serves nothing yet", stats, err)
	}
	if err := svc.syncDecoy(ctx, rt, node, stats, time.Now()); err != nil {
		t.Fatalf("syncDecoy: %v", err)
	}

	stats, err = client.Stats(ctx)
	if err != nil || stats.DecoyRevision != agentproto.DecoyRevision(servedSpec(t)) {
		t.Fatalf("stats = %+v, %v; want the agent to report the revision of the decoy it took", stats, err)
	}
	patch, err := (&NodeService{}).Probe(ctx, node)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if patch.DecoyState == nil || patch.DecoyState.State != agentproto.DecoyStateStarting {
		t.Fatalf("decoy state = %+v, want starting: the agent was not running its loop", patch.DecoyState)
	}

	// With the decoy held the next tick has nothing to send, and taking it off goes the same way.
	if err := svc.syncDecoy(ctx, rt, node, stats, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("syncDecoy with the decoy held: %v", err)
	}
	node.Decoy = model.NodeDecoy{}
	if err := svc.syncDecoy(ctx, rt, node, stats, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("syncDecoy with the decoy taken off: %v", err)
	}
	if stats, err = client.Stats(ctx); err != nil || stats.DecoyRevision != "" {
		t.Fatalf("stats = %+v, %v; want the agent to serve none once the node asks for none", stats, err)
	}
}
