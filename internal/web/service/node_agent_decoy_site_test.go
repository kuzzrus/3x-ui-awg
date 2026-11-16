package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// syncSiteAgent is the decoy site endpoint of the stand-in agent, which the fixture answers with a 404 until a test
// sets one, as an agent that predates it would.
type syncSiteAgent struct {
	puts    []syncSitePut
	status  int // answer every PUT with this status and message instead of taking the site
	message string
}

type syncSitePut struct {
	digest string
	body   []byte
	length int64
}

// serveDecoySite answers the decoy site endpoint, with f.mu held; false means the request is not for it.
func (f *syncAgentFixture) serveDecoySite(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.PathDecoySite || r.Method != http.MethodPut || f.site == nil {
		return false
	}
	body, _ := io.ReadAll(r.Body)
	digest := r.URL.Query().Get(agentproto.QueryDecoySiteSha256)
	f.site.puts = append(f.site.puts, syncSitePut{digest: digest, body: body, length: r.ContentLength})
	if f.site.status != 0 {
		w.WriteHeader(f.site.status)
		_ = json.NewEncoder(w).Encode(agentproto.ErrorBody{Error: f.site.message})
		return true
	}
	_ = json.NewEncoder(w).Encode(agentproto.DecoySite{Sha256: digest, Size: int64(len(body))})
	return true
}

func (f *syncAgentFixture) sitePuts() []syncSitePut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]syncSitePut(nil), f.site.puts...)
}

// awaitSite waits for the send in the background to end, which is when the node may be asked about its site again.
func (s *AgentSyncService) awaitSite(t *testing.T, nodeID int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		s.mu.Lock()
		sending := s.stateFor(nodeID).sendingSite
		s.mu.Unlock()
		if !sending {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the send of the decoy site did not end")
		}
	}
}

func (s *AgentSyncService) retrySiteNow(nodeID int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateFor(nodeID).siteRetryAt = time.Time{}
}

// siteFixture is an agent node that is to serve an uploaded site, which the panel holds.
type siteFixture struct {
	*syncAgentFixture
	digest string
	raw    []byte
	spec   agentproto.DecoySpec
}

func newSiteFixture(t *testing.T) *siteFixture {
	t.Helper()
	f := newSyncAgentFixture(t)
	t.Setenv("XUI_DB_FOLDER", t.TempDir())
	f.site, f.decoy = &syncSiteAgent{}, &syncDecoyAgent{}
	t.Cleanup(func() { setAgentDecoyProblem(f.nodeID, "") })

	raw := decoySiteZip(t, map[string]string{"index.html": "<html>home</html>", "404.html": "<html>gone</html>"})
	site, err := (&DecoySiteService{}).Add("blog.zip", bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	decoy := model.NodeDecoy{Mode: agentproto.DecoyUpload, Site: site.Sha256, Domain: "node.example", Cert: "auto", Port: 7443}
	f.wantDecoy(decoy)
	want, err := normalizeNodeDecoy(decoy)
	if err != nil {
		t.Fatal(err)
	}
	return &siteFixture{syncAgentFixture: f, digest: site.Sha256, raw: raw, spec: decoySpecOf(want)}
}

// An agent is told to serve a site it has not got, so the site goes first, and the decoy once the agent reports it holds it.
func TestAgentSyncSendsTheSiteBeforeTheDecoyThatNamesIt(t *testing.T) {
	f := newSiteFixture(t)
	svc := &AgentSyncService{}

	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	svc.awaitSite(t, f.nodeID)
	puts := f.sitePuts()
	if len(puts) != 1 || puts[0].digest != f.digest || !bytes.Equal(puts[0].body, f.raw) || puts[0].length != int64(len(f.raw)) {
		t.Fatalf("site PUTs = %d, want one that carries the zip as it is, named by its digest, with its length", len(puts))
	}
	if decoys := f.decoyPuts(); len(decoys) != 0 {
		t.Fatalf("decoy PUTs = %+v before the agent holds the site, want none: it would be refused", decoys)
	}
	if problem := agentDecoyProblems.get(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the site was taken", problem)
	}

	if err := f.sync(svc, &agentproto.Stats{DecoySites: []string{f.digest}}); err != nil {
		t.Fatalf("Sync with the site held: %v", err)
	}
	if decoys := f.decoyPuts(); len(decoys) != 1 || decoys[0] != f.spec {
		t.Fatalf("decoy PUTs = %+v, want one that names the site", decoys)
	}
	if puts := f.sitePuts(); len(puts) != 1 {
		t.Fatalf("site PUTs = %d after the agent held it, want the one from before", len(puts))
	}
}

func TestAgentSyncLeavesASiteAndADecoyAnAgentHolds(t *testing.T) {
	f := newSiteFixture(t)
	held := &agentproto.Stats{DecoyRevision: agentproto.DecoyRevision(f.spec), DecoySites: []string{f.digest}}

	svc := &AgentSyncService{}
	if err := f.sync(svc, held); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	svc.awaitSite(t, f.nodeID)
	if puts, decoys := f.sitePuts(), f.decoyPuts(); len(puts) != 0 || len(decoys) != 0 {
		t.Fatalf("PUTs = %d site and %+v decoy, want none while the agent has both", len(puts), decoys)
	}
}

// The agent keeps the decoy it was told across a restart, and the folder of a site can be lost on its own.
func TestAgentSyncSendsASiteAnAgentLostThoughItStillHoldsTheDecoy(t *testing.T) {
	f := newSiteFixture(t)
	lost := &agentproto.Stats{DecoyRevision: agentproto.DecoyRevision(f.spec)}

	svc := &AgentSyncService{}
	if err := f.sync(svc, lost); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	svc.awaitSite(t, f.nodeID)
	if puts := f.sitePuts(); len(puts) != 1 || puts[0].digest != f.digest {
		t.Fatalf("site PUTs = %d, want the site sent again, which is what makes the decoy start", len(puts))
	}
}

func TestAgentSyncTellsWhyASiteCannotBeSent(t *testing.T) {
	for name, tc := range map[string]struct {
		prepare func(f *siteFixture)
		want    string
		sent    int // site PUTs that reached the agent
	}{
		"an agent that predates it": {
			prepare: func(f *siteFixture) { f.site = nil },
			want:    agentTooOldForDecoy,
		},
		"an agent that refuses it": {
			prepare: func(f *siteFixture) {
				f.site.status, f.site.message = http.StatusBadRequest, "archive has no usable index.html"
			},
			want: "the agent refused the decoy site: archive has no usable index.html",
			sent: 1,
		},
		"an agent that finds it too large": {
			prepare: func(f *siteFixture) {
				f.site.status, f.site.message = http.StatusRequestEntityTooLarge, "a decoy site is at most 64 bytes"
			},
			want: "the agent refused the decoy site: a decoy site is at most 64 bytes",
			sent: 1,
		},
		"a zip the panel lost": {
			prepare: func(f *siteFixture) { _ = os.Remove(decoySiteZipPath(f.digest)) },
			want:    errDecoySiteMissing.Error(),
		},
		"a zip the panel damaged": {
			prepare: func(f *siteFixture) { _ = os.WriteFile(decoySiteZipPath(f.digest), []byte("cut short"), 0o600) },
			want:    errDecoySiteDamaged.Error(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSiteFixture(t)
			tc.prepare(f)
			svc := &AgentSyncService{}
			if err := f.sync(svc, &agentproto.Stats{}); err != nil {
				t.Fatalf("Sync: %v", err)
			}
			svc.awaitSite(t, f.nodeID)
			if got := agentDecoyProblems.get(f.nodeID); got != tc.want {
				t.Fatalf("problem = %q, want %q", got, tc.want)
			}
			sent := 0
			if f.site != nil {
				sent = len(f.sitePuts())
			}
			if sent != tc.sent || len(f.decoyPuts()) != 0 {
				t.Fatalf("%d site PUTs and %+v decoy PUTs, want %d and no decoy", sent, f.decoyPuts(), tc.sent)
			}
		})
	}
}

// An agent that turned a site down is left alone for a while, not sent the same megabytes on every tick.
func TestAgentSyncLeavesAnAgentThatRefusedTheSiteAloneForAWhile(t *testing.T) {
	f := newSiteFixture(t)
	f.site.status, f.site.message = http.StatusBadRequest, "no"
	svc := &AgentSyncService{}

	for range 3 {
		if err := f.sync(svc, &agentproto.Stats{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		svc.awaitSite(t, f.nodeID)
	}
	if puts := f.sitePuts(); len(puts) != 1 {
		t.Fatalf("site PUTs = %d over three ticks, want the one, then a wait", len(puts))
	}

	svc.retrySiteNow(f.nodeID)
	f.site.status = 0
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync after the wait: %v", err)
	}
	svc.awaitSite(t, f.nodeID)
	if puts := f.sitePuts(); len(puts) != 2 {
		t.Fatalf("site PUTs = %d after the wait, want it tried again", len(puts))
	}
	if problem := agentDecoyProblems.get(f.nodeID); problem != "" {
		t.Fatalf("problem = %q after the agent took the site, want it cleared", problem)
	}
}

// A link that failed is not a refusal: it is tried again after a longer wait each time, and nothing is shown as wrong with the node.
func TestAgentSyncRetriesASiteThatDidNotArriveWithGrowingWaits(t *testing.T) {
	f := newSiteFixture(t)
	f.site.status = http.StatusInternalServerError
	svc := &AgentSyncService{}

	var waits []time.Duration
	for range 3 {
		before := time.Now()
		if err := f.sync(svc, &agentproto.Stats{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		svc.awaitSite(t, f.nodeID)
		svc.mu.Lock()
		waits = append(waits, svc.stateFor(f.nodeID).siteRetryAt.Sub(before).Round(time.Second))
		svc.mu.Unlock()
		svc.retrySiteNow(f.nodeID)
	}
	if waits[0] != geoRetryDelay(1) || waits[1] != geoRetryDelay(2) || waits[2] != geoRetryDelay(3) {
		t.Fatalf("waits after failures = %v, want %v, %v, %v", waits, geoRetryDelay(1), geoRetryDelay(2), geoRetryDelay(3))
	}
	if problem := agentDecoyProblems.get(f.nodeID); problem != "" {
		t.Fatalf("problem = %q, want none for a transfer that failed on the way", problem)
	}
}

// A send that is on its way is not started a second time by the next tick, which comes long before a slow link is done.
func TestAgentSyncDoesNotSendASiteThatIsOnItsWay(t *testing.T) {
	f := newSiteFixture(t)
	svc := &AgentSyncService{}
	svc.mu.Lock()
	svc.stateFor(f.nodeID).sendingSite = true
	svc.mu.Unlock()

	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	if puts := f.sitePuts(); len(puts) != 0 {
		t.Fatalf("site PUTs = %d while a send was on its way, want none", len(puts))
	}
}

// An agent that took the site and still lists none is not sent the same megabytes on every tick.
func TestAgentSyncDoesNotResendASiteRightAfterTheAgentTookIt(t *testing.T) {
	f := newSiteFixture(t)
	svc := &AgentSyncService{}

	for range 3 {
		if err := f.sync(svc, &agentproto.Stats{}); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		svc.awaitSite(t, f.nodeID)
	}
	if puts := f.sitePuts(); len(puts) != 1 {
		t.Fatalf("site PUTs = %d over three ticks of an agent that lists no site, want the one, then a wait", len(puts))
	}

	svc.retrySiteNow(f.nodeID)
	if err := f.sync(svc, &agentproto.Stats{}); err != nil {
		t.Fatalf("Sync after the wait: %v", err)
	}
	svc.awaitSite(t, f.nodeID)
	if puts := f.sitePuts(); len(puts) != 2 {
		t.Fatalf("site PUTs = %d after the wait, want it sent again", len(puts))
	}
}
