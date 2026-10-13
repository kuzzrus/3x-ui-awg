package controller

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"github.com/mhsanaei/3x-ui/v3/internal/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// pullPanel is a panel with the node endpoints and the one the agents dial, behind TLS and a base path.
type pullPanel struct {
	t      *testing.T
	engine *gin.Engine
	server *httptest.Server
}

func newPullPanel(t *testing.T) *pullPanel {
	t.Helper()
	engine := newNodeCredentialTestEngine(t)
	NewAgentConnectController(engine.Group("/base"))
	server := httptest.NewTLSServer(engine)
	t.Cleanup(server.Close)
	return &pullPanel{t: t, engine: engine, server: server}
}

func (p *pullPanel) masterURL() string { return p.server.URL + "/base/" }

func (p *pullPanel) pin() string {
	sum := sha256.Sum256(p.server.Certificate().Raw)
	return hex.EncodeToString(sum[:])
}

// addPullNode adds a node through the panel's own endpoint and returns the bundle it hands out.
func (p *pullPanel) addPullNode(name string) (int, *agentproto.Bundle) {
	p.t.Helper()
	w := postForm(p.t, p.engine, "/panel/api/nodes/addAgent", url.Values{
		"name": {name}, "address": {"203.0.113.7"}, "pull": {"true"},
		"masterUrl": {p.masterURL()}, "masterPin": {p.pin()},
	})
	return p.bundleOf(w)
}

func (p *pullPanel) bundleOf(w *httptest.ResponseRecorder) (int, *agentproto.Bundle) {
	p.t.Helper()
	var reply pairingReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.Success {
		p.t.Fatalf("the panel answered %s: %v", w.Body.String(), err)
	}
	bundle, err := agentproto.ParseBundle(reply.Obj.Bundle)
	if err != nil {
		p.t.Fatalf("the bundle does not parse: %v", err)
	}
	id := reply.Obj.Node.Id
	p.t.Cleanup(func() { runtime.GetAgentTunnels().Drop(id) })
	return id, bundle
}

// dial opens the connection an agent opens, with the bearer given, and returns what the panel answered.
func (p *pullPanel) dial(node, bearer string, protocols ...string) (*websocket.Conn, int) {
	p.t.Helper()
	dialer := websocket.Dialer{
		Subprotocols:    protocols,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the test panel's own certificate
	}
	header := http.Header{}
	if bearer != "" {
		header.Set("Authorization", "Bearer "+bearer)
	}
	ws, resp, err := dialer.Dial("wss"+strings.TrimPrefix(p.server.URL, "https")+"/base/agent/connect/"+node, header)
	if err != nil {
		if resp == nil {
			p.t.Fatalf("dial: %v", err)
		}
		return nil, resp.StatusCode
	}
	p.t.Cleanup(func() { ws.Close() })
	return ws, http.StatusSwitchingProtocols
}

func (p *pullPanel) waitConnected(id int, want bool) {
	p.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for runtime.GetAgentTunnels().Connected(id) != want {
		if time.Now().After(deadline) {
			p.t.Fatalf("node %d never became connected=%v", id, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole of pull mode with nothing stood in for: a node added through the panel's endpoint, its bundle handed
// to a real agent, which dials the endpoint the agents dial, and the panel's own client asking it for its status.
func TestAnAgentThatDialsThePanelIsReachedByTheClientOfItsNode(t *testing.T) {
	panel := newPullPanel(t)
	id, bundle := panel.addPullNode("edge")
	if !bundle.Pull() || bundle.Node != id || bundle.Master != panel.masterURL() || bundle.Pin != panel.pin() {
		t.Fatalf("bundle = %+v, want a pull bundle of node %d for %s", bundle, id, panel.masterURL())
	}

	state, err := agent.OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	guid, err := state.Guid()
	if err != nil {
		t.Fatal(err)
	}
	server, err := agent.NewServer(agent.NewCore(state, t.TempDir()), state, bundle.Secret)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.ServePull(ctx, bundle)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	panel.waitConnected(id, true)

	node, err := (&service.NodeService{}).GetById(id)
	if err != nil {
		t.Fatal(err)
	}
	client, err := runtime.NewAgentClient(node, "")
	if err != nil {
		t.Fatalf("NewAgentClient: %v", err)
	}
	callCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	status, err := client.Status(callCtx)
	if err != nil || status.Guid != guid {
		t.Fatalf("Status = %+v, %v; want the guid %s of the agent", status, err, guid)
	}
	stats, err := client.Stats(callCtx)
	if err != nil || stats == nil {
		t.Fatalf("Stats = %+v, %v", stats, err)
	}

	// The panel's own heartbeat fills the same fields from it, as it does for an agent it dials.
	patch, err := (&service.NodeService{}).Probe(callCtx, node)
	if err != nil || patch.LastError != "" || patch.Guid != guid {
		t.Fatalf("Probe = %+v, %v; want the guid %s and no error", patch, err, guid)
	}
}

// Whoever dials without the secret of a node that dials the panel, and whoever names a node that does not,
// is answered the same: a bare 404.
func TestTheEndpointAgentsDialAnswersEveryoneElseWithABare404(t *testing.T) {
	panel := newPullPanel(t)
	id, bundle := panel.addPullNode("edge")
	pushed := postForm(t, panel.engine, "/panel/api/nodes/addAgent", url.Values{"name": {"pushed"}, "address": {"203.0.113.8"}, "port": {"8443"}})
	pushID, pushBundle := panel.bundleOf(pushed)
	idText := strconv.Itoa(id)

	if _, status := panel.dial(idText, "", agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("no secret: %d, want 404", status)
	}
	if _, status := panel.dial(idText, "not-the-secret", agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("wrong secret: %d, want 404", status)
	}
	if _, status := panel.dial("9999", bundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("a node that does not exist: %d, want 404", status)
	}
	if _, status := panel.dial("abc", bundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("an id that is no number: %d, want 404", status)
	}
	if _, status := panel.dial(strconv.Itoa(pushID), pushBundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("a node the panel dials: %d, want 404 even with its own secret", status)
	}
	if _, status := panel.dial(idText, bundle.Secret); status != http.StatusNotFound {
		t.Errorf("no subprotocol: %d, want 404", status)
	}

	postForm(t, panel.engine, "/panel/api/nodes/setEnable/"+idText, url.Values{"enable": {"false"}})
	if _, status := panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("a node that is disabled: %d, want 404", status)
	}
	postForm(t, panel.engine, "/panel/api/nodes/setEnable/"+idText, url.Values{"enable": {"true"}})
	if _, status := panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol); status != http.StatusSwitchingProtocols {
		t.Errorf("the right secret of a node that dials: %d, want the connection to be taken", status)
	}
	panel.waitConnected(id, true)
}

// A new bundle replaces the secret, and an agent that holds the old one is cut off at once and kept out.
func TestPairingAgainCutsOffTheAgentWithTheOldSecret(t *testing.T) {
	panel := newPullPanel(t)
	id, bundle := panel.addPullNode("edge")
	idText := strconv.Itoa(id)
	if _, status := panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol); status != http.StatusSwitchingProtocols {
		t.Fatalf("first dial: %d", status)
	}
	panel.waitConnected(id, true)

	_, again := panel.bundleOf(postForm(t, panel.engine, "/panel/api/nodes/repairAgent/"+idText, url.Values{"masterUrl": {panel.masterURL()}, "masterPin": {panel.pin()}}))
	if again.Secret == bundle.Secret || !again.Pull() || again.Node != id {
		t.Fatalf("the new bundle = %+v, want a pull bundle of the same node with another secret", again)
	}
	panel.waitConnected(id, false)
	if _, status := panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("the old secret: %d, want 404", status)
	}
	if _, status := panel.dial(idText, again.Secret, agentproto.ConnectProtocol); status != http.StatusSwitchingProtocols {
		t.Errorf("the new secret: %d, want the connection to be taken", status)
	}
}

func TestDeletingANodeOrDisablingItCutsOffItsAgent(t *testing.T) {
	panel := newPullPanel(t)
	id, bundle := panel.addPullNode("edge")
	idText := strconv.Itoa(id)

	panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol)
	panel.waitConnected(id, true)
	postForm(t, panel.engine, "/panel/api/nodes/setEnable/"+idText, url.Values{"enable": {"false"}})
	panel.waitConnected(id, false)

	postForm(t, panel.engine, "/panel/api/nodes/setEnable/"+idText, url.Values{"enable": {"true"}})
	panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol)
	panel.waitConnected(id, true)
	// The edit form has a switch of its own.
	postForm(t, panel.engine, "/panel/api/nodes/update/"+idText, url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"443"}, "enable": {"false"},
	})
	panel.waitConnected(id, false)

	postForm(t, panel.engine, "/panel/api/nodes/setEnable/"+idText, url.Values{"enable": {"true"}})
	panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol)
	panel.waitConnected(id, true)
	postForm(t, panel.engine, "/panel/api/nodes/del/"+idText, nil)
	panel.waitConnected(id, false)
	if _, status := panel.dial(idText, bundle.Secret, agentproto.ConnectProtocol); status != http.StatusNotFound {
		t.Errorf("a node that was deleted: %d, want 404", status)
	}
}

type nodeShape struct {
	Pull             bool   `json:"pull"`
	Port             int    `json:"port"`
	TlsVerifyMode    string `json:"tlsVerifyMode"`
	PinnedCertSha256 string `json:"pinnedCertSha256"`
}

func shapeOf(t *testing.T, engine http.Handler, id int) nodeShape {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/api/nodes/get/"+strconv.Itoa(id), nil))
	var reply struct {
		Success bool      `json:"success"`
		Obj     nodeShape `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.Success {
		t.Fatalf("get answered %d %s: %v", w.Code, w.Body.String(), err)
	}
	return reply.Obj
}

// A node moves from one way of being reached to the other by being paired again, and keeps its id, so the
// inbounds that were put on it stay on it.
func TestAnAgentNodeIsPairedAgainTheOtherWay(t *testing.T) {
	panel := newPullPanel(t)
	pushed := postForm(t, panel.engine, "/panel/api/nodes/addAgent", url.Values{"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"}})
	id, pushBundle := panel.bundleOf(pushed)
	idText := strconv.Itoa(id)
	if pushBundle.Pull() {
		t.Fatalf("a node added without pull got a pull bundle: %+v", pushBundle)
	}
	if got := shapeOf(t, panel.engine, id); got.Pull || got.Port != 8443 || got.TlsVerifyMode != "pin" || got.PinnedCertSha256 == "" {
		t.Fatalf("a node the panel dials = %+v", got)
	}

	// To pull: the master's address is what is needed, and the pin of the agent's certificate goes.
	if w := postFormAnyStatus(panel.engine, "/panel/api/nodes/repairAgent/"+idText, url.Values{"pull": {"true"}}); succeeded(t, w) {
		t.Fatal("a node was paired to dial the master without being told where the master is")
	}
	_, pullBundle := panel.bundleOf(postForm(t, panel.engine, "/panel/api/nodes/repairAgent/"+idText, url.Values{
		"pull": {"true"}, "masterUrl": {panel.masterURL()}, "masterPin": {panel.pin()},
	}))
	if !pullBundle.Pull() || pullBundle.Node != id {
		t.Fatalf("bundle = %+v, want a pull bundle of node %d", pullBundle, id)
	}
	if got := shapeOf(t, panel.engine, id); !got.Pull || got.TlsVerifyMode != "verify" || got.PinnedCertSha256 != "" {
		t.Fatalf("a node whose agent dials the panel = %+v, want the pin of the agent cleared", got)
	}
	// It is still an ordinary node to edit: nothing that is left of the old way makes a save fail.
	form := url.Values{"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"443"}, "enable": {"true"}, "remark": {"edited"}}
	if w := postFormAnyStatus(panel.engine, "/panel/api/nodes/update/"+idText, form); !succeeded(t, w) {
		t.Fatalf("saving a node whose agent dials the panel failed: %s", w.Body.String())
	}
	if got := shapeOf(t, panel.engine, id); !got.Pull {
		t.Fatalf("a save changed the way the node is reached: %+v", got)
	}

	// Back to push: the port of the agent is what is needed, since the one the node holds was only a placeholder.
	if w := postFormAnyStatus(panel.engine, "/panel/api/nodes/repairAgent/"+idText, url.Values{"pull": {"false"}}); succeeded(t, w) {
		t.Fatal("a node was paired to be dialled without a port to dial")
	}
	_, backBundle := panel.bundleOf(postForm(t, panel.engine, "/panel/api/nodes/repairAgent/"+idText, url.Values{"pull": {"false"}, "port": {"9443"}}))
	if backBundle.Pull() || backBundle.Port != 9443 {
		t.Fatalf("bundle = %+v, want a push bundle for port 9443", backBundle)
	}
	if got := shapeOf(t, panel.engine, id); got.Pull || got.Port != 9443 || got.TlsVerifyMode != "pin" || got.PinnedCertSha256 == "" {
		t.Fatalf("a node the panel dials again = %+v", got)
	}
}

// postFormAnyStatus is postForm for a request that may be turned down, which the panel does with a 200 and an error in the body.
func postFormAnyStatus(engine http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	engine.ServeHTTP(w, req)
	return w
}
