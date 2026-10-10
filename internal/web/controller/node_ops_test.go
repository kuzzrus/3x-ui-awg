package controller

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func post(t *testing.T, engine http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("")))
	return w.Code, w.Body.String()
}

func seedNode(t *testing.T, node *model.Node) int {
	t.Helper()
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	return node.Id
}

func TestNodeControllerRefusesALogRequestItCannotRead(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	panel := seedNode(t, &model.Node{Name: "panel", Scheme: "https", Address: "example.com", Port: 2053, BasePath: "/", Enable: true})
	agent := seedNode(t, &model.Node{Name: "agent", Kind: model.NodeKindAgent, Address: "203.0.113.7", Port: 8443, Enable: true})

	for name, path := range map[string]string{
		"an id that is no number":     "/panel/api/nodes/agentLogs/abc/20",
		"a count that is no number":   "/panel/api/nodes/agentLogs/" + strconv.Itoa(agent) + "/many",
		"a count of zero":             "/panel/api/nodes/agentLogs/" + strconv.Itoa(agent) + "/0",
		"a node that does not exist":  "/panel/api/nodes/agentLogs/9999/20",
		"a node that is not an agent": "/panel/api/nodes/agentLogs/" + strconv.Itoa(panel) + "/20",
	} {
		code, body := post(t, engine, path)
		if code != http.StatusOK || !strings.Contains(body, `"success":false`) {
			t.Errorf("%s: answered %d %s, want a failure in the usual envelope", name, code, body)
		}
	}
}

func TestNodeControllerRefusesToRestartWhatItCannot(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	off := seedNode(t, &model.Node{Name: "off", Kind: model.NodeKindAgent, Address: "203.0.113.8", Port: 8443, Enable: false})
	if err := database.GetDB().Model(&model.Node{}).Where("id = ?", off).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"an id that is no number":     "/panel/api/nodes/restartXray/abc",
		"a node that does not exist":  "/panel/api/nodes/restartXray/9999",
		"a node that is switched off": "/panel/api/nodes/restartXray/" + strconv.Itoa(off),
	} {
		code, body := post(t, engine, path)
		if code != http.StatusOK || !strings.Contains(body, `"success":false`) {
			t.Errorf("%s: answered %d %s, want a failure in the usual envelope", name, code, body)
		}
	}
}
