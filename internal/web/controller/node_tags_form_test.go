package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func postForm(t *testing.T, engine http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("%s answered %d: %s", path, w.Code, w.Body.String())
	}
	return w
}

// tagsOf reads the tags the panel shows for a node, which must be a list even when it is empty.
func tagsOf(t *testing.T, engine http.Handler, id int) []string {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/api/nodes/get/"+strconv.Itoa(id), nil))
	var reply struct {
		Success bool `json:"success"`
		Obj     struct {
			Tags *[]string `json:"tags"`
		} `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.Success {
		t.Fatalf("get answered %d %s: %v", w.Code, w.Body.String(), err)
	}
	if reply.Obj.Tags == nil {
		t.Fatalf("the node has no tags field, or a null one: %s", w.Body.String())
	}
	return *reply.Obj.Tags
}

// The panel's own pages post a node as a form, where a list is a repeated key and an empty list is no
// key at all, so a save that names no tags is how they clear them.
func TestNodeControllerTakesAnAgentsTagsAsAForm(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)

	w := postForm(t, engine, "/panel/api/nodes/addAgent", url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"}, "tags": {"eu", " exit "},
	})
	var added pairingReply
	if err := json.Unmarshal(w.Body.Bytes(), &added); err != nil || !added.Success {
		t.Fatalf("addAgent answered %s: %v", w.Body.String(), err)
	}
	id := added.Obj.Node.Id
	if got, want := tagsOf(t, engine, id), []string{"eu", "exit"}; !slices.Equal(got, want) {
		t.Fatalf("tags after adding = %q, want %q", got, want)
	}

	save := func(tags ...string) []string {
		t.Helper()
		form := url.Values{"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"}, "enable": {"true"}}
		if len(tags) > 0 {
			form["tags"] = tags
		}
		postForm(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(id), form)
		return tagsOf(t, engine, id)
	}
	if got, want := save("us"), []string{"us"}; !slices.Equal(got, want) {
		t.Fatalf("tags after naming one = %q, want %q", got, want)
	}
	if got := save(); len(got) != 0 {
		t.Fatalf("tags after a save that names none = %q, want them cleared", got)
	}
}
