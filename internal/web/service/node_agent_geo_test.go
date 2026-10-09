package service

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// syncGeoAgent is the geo endpoint of the stand-in agent. The fixture answers it with a 404 until a
// test sets one, as an agent from before the endpoint does.
type syncGeoAgent struct {
	held     []string
	puts     []syncGeoPut
	gets     int
	attempts int
	broken   bool          // answer the PUTs with a 500
	gate     chan struct{} // when set, a PUT waits until it is closed
}

type syncGeoPut struct {
	name, digest string
	content      []byte // as the agent unpacked it
}

// serveGeo answers the geo endpoint, with f.mu held; false means the request is not for it.
func (f *syncAgentFixture) serveGeo(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.Path != agentproto.PathGeo || f.geo == nil {
		return false
	}
	geo := f.geo
	switch r.Method {
	case http.MethodGet:
		geo.gets++
		files := []agentproto.GeoFile{}
		for _, name := range geo.held {
			files = append(files, agentproto.GeoFile{Name: name, Size: 1})
		}
		_ = json.NewEncoder(w).Encode(agentproto.GeoFiles{Files: files})
	case http.MethodPut:
		raw, _ := io.ReadAll(r.Body)
		geo.attempts++
		if geo.gate != nil {
			f.mu.Unlock()
			<-geo.gate
			f.mu.Lock()
		}
		if geo.broken {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"no space left on device"}`))
			return true
		}
		zr, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			f.t.Errorf("the geo file did not arrive gzipped: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return true
		}
		content, _ := io.ReadAll(zr)
		query := r.URL.Query()
		name := query.Get(agentproto.QueryGeoName)
		geo.puts = append(geo.puts, syncGeoPut{name: name, digest: query.Get(agentproto.QueryGeoSha256), content: content})
		geo.held = append(geo.held, name)
		_ = json.NewEncoder(w).Encode(agentproto.GeoFile{Name: name, Size: int64(len(content))})
	default:
		return false
	}
	return true
}

func (f *syncAgentFixture) geoPuts() []syncGeoPut {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.geo.puts)
}

func (f *syncAgentFixture) geoCalls() (gets, attempts int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.geo.gets, f.geo.attempts
}

// waitForGeoSend waits until no geo file is on its way to the node.
func waitForGeoSend(t *testing.T, svc *AgentSyncService, nodeID int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		svc.mu.Lock()
		busy := svc.stateFor(nodeID).sendingGeo
		svc.mu.Unlock()
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the geo files were still on their way after ten seconds")
		}
	}
}

// masterGeoFiles makes a folder the master reads its geo files from, holding the given ones.
func masterGeoFiles(t *testing.T, files map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XUI_BIN_FOLDER", dir)
	t.Setenv("XRAY_LOCATION_ASSET", "")
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// templateReadsGeoFile adds a rule to the template that matches addresses in a geo file.
func templateReadsGeoFile(t *testing.T, file string) {
	t.Helper()
	settings := &SettingService{}
	template, err := settings.GetXrayConfigTemplate()
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(template), &parsed); err != nil {
		t.Fatal(err)
	}
	routing := parsed["routing"].(map[string]any)
	rules, _ := routing["rules"].([]any)
	routing["rules"] = append(rules, map[string]any{"type": "field", "ip": []string{"ext:" + file + ":x"}, "outboundTag": "direct"})
	raw, err := json.Marshal(parsed)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.saveSetting("xrayTemplateConfig", string(raw)); err != nil {
		t.Fatal(err)
	}
}

func TestAgentGeoFilesNeededReadsWhatTheCoreReads(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   []string
	}{
		{"nothing", `{"routing":{"rules":[]}}`, nil},
		{"the default pair", `{"ip":["geoip:private"],"domain":["geosite:google"]}`, []string{"geoip.dat", "geosite.dat"}},
		{"only the one used", `{"ip":["geoip:!cn"]}`, []string{"geoip.dat"}},
		{
			"files named by ext entries",
			`{"domain":["ext:geosite_runet.dat:ru-blocked"],"ip":["ext:geoip_runet.dat:ru","ext-ip:geoip_IR.dat:ir"]}`,
			[]string{"geoip_IR.dat", "geoip_runet.dat", "geosite_runet.dat"},
		},
		{
			"files of the geodata section",
			`{"geodata":{"cron":"0 4 * * *","assets":[{"url":"https://example.com/geosite.dat","file":"geosite_probe.dat"}]}}`,
			[]string{"geosite_probe.dat"},
		},
		{
			"names that are no geo file are left out",
			`{"geodata":{"assets":[{"file":"../evil.dat"},{"file":"notes.txt"},{"file":""}]},"x":"ext:a/b.dat:c"}`,
			nil,
		},
		{"words that only end in ext", `{"x":"next:abc.dat:y","y":"context:abc.dat"}`, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentGeoFilesNeeded([]byte(tt.config))
			if len(got) != len(tt.want) || (len(got) > 0 && !slices.Equal(got, tt.want)) {
				t.Fatalf("needed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAgentSyncSendsTheGeoFilesAConfigNeedsBeforeTheConfig(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.geo = &syncGeoAgent{held: []string{"geoip.dat", "geosite.dat"}}
	content := bytes.Repeat([]byte("regional geo data "), 4000)
	masterGeoFiles(t, map[string][]byte{"geoip_x.dat": content})
	templateReadsGeoFile(t, "geoip_x.dat")
	svc := &AgentSyncService{}
	f.markDirty()

	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 0 {
		t.Fatalf("%d config pushes before the geo file was there, want none: the core would refuse it", got)
	}
	waitForGeoSend(t, svc, f.nodeID)
	sum := sha256.Sum256(content)
	puts := f.geoPuts()
	if len(puts) != 1 || puts[0].name != "geoip_x.dat" || !bytes.Equal(puts[0].content, content) || puts[0].digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("geo files sent = %d, want geoip_x.dat once with its content and digest", len(puts))
	}

	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync after the geo file arrived: %v", err)
	}
	pushes := f.pushed()
	if len(pushes) != 1 || string(pushes[0].body) != string(f.wantRendered()) {
		t.Fatalf("pushes = %d, want the rendered config on the first tick after the file", len(pushes))
	}
	if f.node().ConfigDirty {
		t.Fatal("the node is still dirty after the agent took the config")
	}
	if _, attempts := f.geoCalls(); attempts != 1 {
		t.Fatalf("%d uploads in all, want the one", attempts)
	}
}

func TestAgentSyncLeavesAGeoFileTheAgentHolds(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.geo = &syncGeoAgent{held: []string{"geoip.dat", "geosite.dat", "geoip_x.dat"}}
	masterGeoFiles(t, map[string][]byte{"geoip_x.dat": []byte("newer on the master")})
	templateReadsGeoFile(t, "geoip_x.dat")
	f.markDirty()

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes, want the config at once", got)
	}
	if _, attempts := f.geoCalls(); attempts != 0 {
		t.Fatalf("%d uploads of a file the agent holds, want none: keeping it current is the core's geodata section", attempts)
	}
}

func TestAgentSyncPushesTheConfigWhenThePanelHasNoCopyOfTheGeoFile(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.geo = &syncGeoAgent{held: []string{"geoip.dat", "geosite.dat"}}
	masterGeoFiles(t, nil)
	templateReadsGeoFile(t, "geoip_x.dat")
	f.markDirty()

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes, want the config tried, so that the agent's refusal reaches the operator", got)
	}
	if _, attempts := f.geoCalls(); attempts != 0 {
		t.Fatalf("%d uploads of a file the panel does not have", attempts)
	}
}

func TestAgentSyncPushesAsBeforeToAnAgentThatPredatesGeoFiles(t *testing.T) {
	f := newSyncAgentFixture(t)
	masterGeoFiles(t, map[string][]byte{"geoip_x.dat": []byte("data")})
	templateReadsGeoFile(t, "geoip_x.dat")
	f.markDirty()

	if err := f.sync(&AgentSyncService{}, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if got := len(f.pushed()); got != 1 {
		t.Fatalf("%d pushes to an agent that answers 404 to the geo endpoint, want the config at once", got)
	}
}

func TestAgentSyncRetriesAGeoFileTheAgentCouldNotStore(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.geo = &syncGeoAgent{held: []string{"geoip.dat", "geosite.dat"}, broken: true}
	masterGeoFiles(t, map[string][]byte{"geoip_x.dat": []byte("data")})
	templateReadsGeoFile(t, "geoip_x.dat")
	svc := &AgentSyncService{}
	f.markDirty()

	if err := f.sync(svc, nil); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	waitForGeoSend(t, svc, f.nodeID)
	if _, attempts := f.geoCalls(); attempts != 1 || len(f.pushed()) != 0 {
		t.Fatalf("uploads = %d, pushes = %d, want the one failed upload and no push", attempts, len(f.pushed()))
	}

	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if _, attempts := f.geoCalls(); attempts != 1 {
		t.Fatalf("%d uploads right after a failed one, want the wait before another", attempts)
	}

	f.mu.Lock()
	f.geo.broken = false
	f.mu.Unlock()
	svc.agents[f.nodeID].checkedAt = time.Now().Add(-agentDriftCheckEvery)
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	waitForGeoSend(t, svc, f.nodeID)
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.geoPuts()); got != 1 || len(f.pushed()) != 1 {
		t.Fatalf("stored files = %d, pushes = %d, want the file stored and the config pushed after it", got, len(f.pushed()))
	}
}

func TestAgentSyncStartsNoSecondSendWhileGeoFilesAreOnTheirWay(t *testing.T) {
	f := newSyncAgentFixture(t)
	f.geo = &syncGeoAgent{held: []string{"geoip.dat", "geosite.dat"}, gate: make(chan struct{})}
	masterGeoFiles(t, map[string][]byte{"geoip_x.dat": []byte("data")})
	templateReadsGeoFile(t, "geoip_x.dat")
	svc := &AgentSyncService{}
	f.markDirty()

	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if _, attempts := f.geoCalls(); attempts == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the upload never reached the agent")
		}
	}

	// A mutation and the end of the check interval both ask for a look while the file is still moving.
	f.markDirty()
	svc.agents[f.nodeID].checkedAt = time.Now().Add(-agentDriftCheckEvery)
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	gets, attempts := f.geoCalls()
	if gets != 1 || attempts != 1 || len(f.pushed()) != 0 {
		t.Fatalf("lists = %d, uploads = %d, pushes = %d, want the tick to leave the node to the upload", gets, attempts, len(f.pushed()))
	}

	close(f.geo.gate)
	waitForGeoSend(t, svc, f.nodeID)
	if err := f.sync(svc, nil); err != nil {
		t.Fatal(err)
	}
	if got := len(f.geoPuts()); got != 1 || len(f.pushed()) != 1 {
		t.Fatalf("stored files = %d, pushes = %d, want one of each", got, len(f.pushed()))
	}
}
