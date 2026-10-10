package service

import (
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

// syncUpdateAgent is the update endpoint of the stand-in agent. The fixture answers it with a 404
// until a test sets one.
type syncUpdateAgent struct {
	started []string // the dev flag of each POST
	polls   int
	refuse  string   // answer the POST with a 409 and this reason
	results []string // the states the GETs report, the last one repeated
}

// serveUpdate answers the update endpoint, with f.mu held; false means the request is not for it.
func (f *syncAgentFixture) serveUpdate(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.PathUpdate || f.update == nil {
		return false
	}
	update := f.update
	switch r.Method {
	case http.MethodPost:
		if update.refuse != "" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(agentproto.ErrorBody{Error: update.refuse})
			return true
		}
		update.started = append(update.started, r.URL.Query().Get(agentproto.QueryUpdateDev))
		_ = json.NewEncoder(w).Encode(agentproto.UpdateStatus{RunID: "42", State: agentproto.UpdatePending})
	case http.MethodGet:
		state := agentproto.UpdatePending
		if len(update.results) > 0 {
			state = update.results[min(update.polls, len(update.results)-1)]
		}
		update.polls++
		_ = json.NewEncoder(w).Encode(agentproto.UpdateStatus{RunID: "42", State: state})
	default:
		return false
	}
	return true
}

func (f *syncAgentFixture) updateCalls() (started []string, polls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.update.started...), f.update.polls
}

// useManagerWith makes the agent of the fixture the one the runtime manager hands out.
func useManagerWith(t *testing.T, f *syncAgentFixture) {
	t.Helper()
	mgr := runtime.NewManager(runtime.LocalDeps{APIPort: func() int { return 0 }, SetNeedRestart: func() {}})
	prev := runtime.GetManager()
	runtime.SetManager(mgr)
	t.Cleanup(func() { runtime.SetManager(prev) })
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", f.nodeID).Update("status", "online").Error; err != nil {
		t.Fatal(err)
	}
}

func shortWatch(t *testing.T) {
	t.Helper()
	previous := agentUpdateWatchEvery
	agentUpdateWatchEvery = 5 * time.Millisecond
	t.Cleanup(func() { agentUpdateWatchEvery = previous })
}

func TestUpdatePanelsStartsTheSelfUpdateOfAnAgentOnTheChannelAsked(t *testing.T) {
	shortWatch(t)
	f := newSyncAgentFixture(t)
	f.update = &syncUpdateAgent{results: []string{agentproto.UpdatePending, agentproto.UpdateSuccess}}
	useManagerWith(t, f)

	for _, dev := range []bool{false, true} {
		results, err := (&NodeService{}).UpdatePanels([]int{f.nodeID}, dev)
		if err != nil || len(results) != 1 || !results[0].OK {
			t.Fatalf("UpdatePanels(dev=%v) = %+v, %v, want the update started", dev, results, err)
		}
	}
	started, _ := f.updateCalls()
	if len(started) != 2 || started[0] != "false" || started[1] != "true" {
		t.Fatalf("updates started with dev = %v, want false and then true", started)
	}
}

func TestUpdatePanelsWatchesAnAgentUpdateUntilItEnds(t *testing.T) {
	shortWatch(t)
	f := newSyncAgentFixture(t)
	f.update = &syncUpdateAgent{results: []string{agentproto.UpdatePending, agentproto.UpdatePending, agentproto.UpdateFailed}}
	useManagerWith(t, f)

	if _, err := (&NodeService{}).UpdatePanels([]int{f.nodeID}, false); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, polls := f.updateCalls(); polls >= 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the master stopped asking before the update had ended")
		}
	}
	time.Sleep(10 * agentUpdateWatchEvery)
	if _, polls := f.updateCalls(); polls != 3 {
		t.Fatalf("%d polls, want it to stop at the one that found the update failed", polls)
	}
}

func TestUpdatePanelsReportsAnAgentThatRefusesTheUpdate(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.update = &syncUpdateAgent{refuse: "an update is already running"}
	useManagerWith(t, f)

	results, err := (&NodeService{}).UpdatePanels([]int{f.nodeID}, false)
	if err != nil || len(results) != 1 || results[0].OK || !strings.Contains(results[0].Error, "already running") {
		t.Fatalf("UpdatePanels = %+v, %v, want the agent's reason", results, err)
	}
}

func TestUpdatePanelsLeavesAnAgentThatIsOffline(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.update = &syncUpdateAgent{}
	useManagerWith(t, f)
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", f.nodeID).Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}

	results, err := (&NodeService{}).UpdatePanels([]int{f.nodeID}, false)
	if err != nil || len(results) != 1 || results[0].OK || results[0].Error != "node is offline" {
		t.Fatalf("UpdatePanels = %+v, %v, want the offline node skipped", results, err)
	}
	if started, _ := f.updateCalls(); len(started) != 0 {
		t.Fatalf("an offline agent was asked to update: %v", started)
	}
}

func TestUpdatePanelsSaysWhenTheAgentIsTooOldToUpdateItself(t *testing.T) {
	f := newSyncAgentFixture(t)
	useManagerWith(t, f)

	results, err := (&NodeService{}).UpdatePanels([]int{f.nodeID}, false)
	if err != nil || len(results) != 1 || results[0].OK || !strings.Contains(results[0].Error, "too old to update itself") {
		t.Fatalf("UpdatePanels = %+v, %v, want the agent said to be too old, not a rejected secret", results, err)
	}
}
