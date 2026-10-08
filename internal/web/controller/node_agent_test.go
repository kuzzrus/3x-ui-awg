package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type pairingReply struct {
	Success bool   `json:"success"`
	Msg     string `json:"msg"`
	Obj     struct {
		Node struct {
			Id          int    `json:"id"`
			Kind        string `json:"kind"`
			HasApiToken bool   `json:"hasApiToken"`
			ConfigDirty bool   `json:"configDirty"`
		} `json:"node"`
		Bundle string `json:"bundle"`
	} `json:"obj"`
}

func postJSON(t *testing.T, engine http.Handler, path, body string) (*httptest.ResponseRecorder, pairingReply) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(w, req)
	var reply pairingReply
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%s answered %d with %q: %v", path, w.Code, w.Body.String(), err)
	}
	return w, reply
}

func TestNodeControllerAddAgentHandsBackTheBundleOnlyOnce(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)

	w, reply := postJSON(t, engine, "/panel/api/nodes/addAgent", `{"name":"edge","address":"203.0.113.7","port":8443}`)
	if w.Code != http.StatusOK || !reply.Success {
		t.Fatalf("addAgent = %d %s", w.Code, w.Body.String())
	}
	bundle, err := agentproto.ParseBundle(reply.Obj.Bundle)
	if err != nil {
		t.Fatalf("the bundle in the response does not parse: %v", err)
	}
	if reply.Obj.Node.Kind != model.NodeKindAgent || !reply.Obj.Node.HasApiToken || !reply.Obj.Node.ConfigDirty {
		t.Fatalf("node in the response = %+v, want a dirty agent that has its secret", reply.Obj.Node)
	}
	if strings.Contains(w.Body.String(), `"apiToken"`) {
		t.Fatalf("the response has an apiToken field: %s", w.Body.String())
	}

	for _, path := range []string{"/panel/api/nodes/list", "/panel/api/nodes/get/" + strconv.Itoa(reply.Obj.Node.Id)} {
		got := httptest.NewRecorder()
		engine.ServeHTTP(got, httptest.NewRequest(http.MethodGet, path, nil))
		if got.Code != http.StatusOK || strings.Contains(got.Body.String(), bundle.Secret) || strings.Contains(got.Body.String(), agentproto.BundlePrefix) {
			t.Fatalf("%s = %d; it must answer without the secret or the bundle: %s", path, got.Code, got.Body.String())
		}
	}
}

func TestNodeControllerAddAgentNeedsAnEndpoint(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)

	for _, body := range []string{
		`{"name":"edge","port":8443}`,
		`{"name":"edge","address":"203.0.113.7"}`,
		`{"address":"203.0.113.7","port":8443}`,
	} {
		w, reply := postJSON(t, engine, "/panel/api/nodes/addAgent", body)
		if reply.Success || reply.Obj.Bundle != "" {
			t.Fatalf("addAgent(%s) = %d %s, want a refusal", body, w.Code, w.Body.String())
		}
	}
	var count int64
	if err := database.GetDB().Model(model.Node{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("%d nodes stored after refused requests, %v", count, err)
	}
}

func TestNodeControllerRepairAgentMintsANewBundle(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	_, added := postJSON(t, engine, "/panel/api/nodes/addAgent", `{"name":"edge","address":"203.0.113.7","port":8443}`)

	w, repaired := postJSON(t, engine, "/panel/api/nodes/repairAgent/"+strconv.Itoa(added.Obj.Node.Id), "")
	if w.Code != http.StatusOK || !repaired.Success {
		t.Fatalf("repairAgent = %d %s", w.Code, w.Body.String())
	}
	if repaired.Obj.Bundle == "" || repaired.Obj.Bundle == added.Obj.Bundle {
		t.Fatal("repairing handed back no new bundle")
	}

	panel := &model.Node{Name: "panel", Scheme: "https", Address: "203.0.113.9", Port: 2053, BasePath: "/", ApiToken: "t", Enable: true}
	if err := database.GetDB().Create(panel).Error; err != nil {
		t.Fatal(err)
	}
	if _, refused := postJSON(t, engine, "/panel/api/nodes/repairAgent/"+strconv.Itoa(panel.Id), ""); refused.Success {
		t.Fatal("repairAgent minted a bundle for a panel node")
	}
	if _, refused := postJSON(t, engine, "/panel/api/nodes/repairAgent/not-a-number", ""); refused.Success {
		t.Fatal("repairAgent accepted an id that is no number")
	}
}
