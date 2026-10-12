package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
)

// agentOnTheWire is what the panel shows of the helper programs and the decoy of a node.
type agentOnTheWire struct {
	Sidecars *[]string `json:"sidecars"`
	Decoy    struct {
		Mode     string `json:"mode"`
		Template string `json:"template"`
		Domain   string `json:"domain"`
		Cert     string `json:"cert"`
		Port     int    `json:"port"`
	} `json:"decoy"`
	DecoyState struct {
		State string `json:"state"`
	} `json:"decoyState"`
}

func nodeOnTheWire(t *testing.T, engine http.Handler, id int) agentOnTheWire {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/api/nodes/get/"+strconv.Itoa(id), nil))
	var reply struct {
		Success bool           `json:"success"`
		Obj     agentOnTheWire `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.Success {
		t.Fatalf("get answered %d %s: %v", w.Code, w.Body.String(), err)
	}
	if reply.Obj.Sidecars == nil {
		t.Fatalf("the node has no list of helper programs, or a null one: %s", w.Body.String())
	}
	return reply.Obj
}

func succeeded(t *testing.T, w *httptest.ResponseRecorder) bool {
	t.Helper()
	var reply struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("answer %s is not a reply: %v", w.Body.String(), err)
	}
	return reply.Success
}

// The pages post a node as a form, so the helper programs are a repeated key and the decoy is one flat
// field each; a save that names none is how they are taken off.
func TestNodeControllerTakesAnAgentsHelperProgramsAndDecoyAsAForm(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)

	w := postForm(t, engine, "/panel/api/nodes/addAgent", url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"},
		"sidecars":  {"tor", "psiphon"},
		"decoyMode": {"template"}, "decoyDomain": {" Node.Example. "},
	})
	var added pairingReply
	if err := json.Unmarshal(w.Body.Bytes(), &added); err != nil || !added.Success {
		t.Fatalf("addAgent answered %s: %v", w.Body.String(), err)
	}
	id := added.Obj.Node.Id
	got := nodeOnTheWire(t, engine, id)
	if !slices.Equal(*got.Sidecars, []string{"tor", "psiphon"}) {
		t.Fatalf("helper programs after adding = %q", *got.Sidecars)
	}
	if got.Decoy.Mode != "template" || got.Decoy.Template != "maintenance" || got.Decoy.Domain != "node.example" || got.Decoy.Cert != "auto" || got.Decoy.Port != 7443 {
		t.Fatalf("decoy after adding = %+v, want the one that was named, normalized", got.Decoy)
	}

	save := func(extra url.Values) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"}, "enable": {"true"}}
		for key, values := range extra {
			form[key] = values
		}
		return postForm(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(id), form)
	}
	save(url.Values{
		"sidecars": {"psiphon"}, "decoyMode": {"proxy"}, "decoyProxyUrl": {"https://site.example"},
		"decoyDomain": {"node.example"}, "decoyCert": {"manual"}, "decoyCertFile": {"/etc/ssl/n.pem"}, "decoyKeyFile": {"/etc/ssl/n.key"}, "decoyPort": {"7500"},
	})
	got = nodeOnTheWire(t, engine, id)
	if !slices.Equal(*got.Sidecars, []string{"psiphon"}) || got.Decoy.Mode != "proxy" || got.Decoy.Cert != "manual" || got.Decoy.Port != 7500 {
		t.Fatalf("after naming others: %+v and %q", got.Decoy, *got.Sidecars)
	}

	if succeeded(t, save(url.Values{"decoyMode": {"template"}, "decoyTemplate": {"no-such-page"}, "decoyDomain": {"node.example"}})) {
		t.Fatal("a decoy with a page the panel does not have was saved")
	}
	if got := nodeOnTheWire(t, engine, id); got.Decoy.Mode != "proxy" {
		t.Fatalf("decoy after a refused save = %+v, want the one from before", got.Decoy)
	}

	save(nil)
	got = nodeOnTheWire(t, engine, id)
	if len(*got.Sidecars) != 0 || got.Decoy.Mode != "" || got.Decoy.Domain != "" || got.Decoy.Port != 0 {
		t.Fatalf("after a save that names none: %+v and %q, want both taken off", got.Decoy, *got.Sidecars)
	}
}
