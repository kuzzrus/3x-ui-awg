package controller

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func siteArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func uploadSite(t *testing.T, engine *gin.Engine, field, filename string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/api/nodes/decoySites/upload", &form)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	return w
}

type siteOnTheWire struct {
	Sha256     string `json:"sha256"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	UploadedAt int64  `json:"uploadedAt"`
}

func listedSites(t *testing.T, engine *gin.Engine) []siteOnTheWire {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/panel/api/nodes/decoySites", nil))
	var reply struct {
		Success bool            `json:"success"`
		Obj     []siteOnTheWire `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil || !reply.Success || reply.Obj == nil {
		t.Fatalf("the list answered %d %s: %v", w.Code, w.Body.String(), err)
	}
	return reply.Obj
}

func deleteSite(t *testing.T, engine *gin.Engine, sha string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/panel/api/nodes/decoySites/del/"+sha, nil))
	return w
}

// A site is uploaded once, a node picks it by its digest in the form it posts, and the panel will not delete a site that a node serves.
func TestNodeControllerKeepsAnUploadedDecoySiteAndLetsANodeServeIt(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	raw := siteArchive(t, map[string]string{"index.html": "<html>home</html>", "404.html": "<html>gone</html>"})
	sum := sha256.Sum256(raw)
	digest := hex.EncodeToString(sum[:])

	w := uploadSite(t, engine, "site", `C:\fakepath\blog.zip`, raw)
	var uploaded struct {
		Success bool          `json:"success"`
		Obj     siteOnTheWire `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &uploaded); err != nil || !uploaded.Success {
		t.Fatalf("the upload answered %d %s: %v", w.Code, w.Body.String(), err)
	}
	if got := uploaded.Obj; got.Sha256 != digest || got.Name != "blog.zip" || got.Size != int64(len(raw)) || got.UploadedAt == 0 {
		t.Fatalf("the upload answered %+v, want the digest, the name without the folders it came from, and the size", got)
	}
	if sites := listedSites(t, engine); len(sites) != 1 || sites[0] != uploaded.Obj {
		t.Fatalf("the list holds %+v, want the site that was uploaded", sites)
	}

	w = postForm(t, engine, "/panel/api/nodes/addAgent", url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"},
		"decoyMode": {"upload"}, "decoySite": {strings.ToUpper(digest)}, "decoyDomain": {"node.example"},
	})
	var added pairingReply
	if err := json.Unmarshal(w.Body.Bytes(), &added); err != nil || !added.Success {
		t.Fatalf("addAgent answered %s: %v", w.Body.String(), err)
	}
	id := added.Obj.Node.Id
	if got := nodeOnTheWire(t, engine, id); got.Decoy.Mode != "upload" || got.Decoy.Site != digest || got.Decoy.Template != "" {
		t.Fatalf("decoy after adding = %+v, want the site named in lower case and no page", got.Decoy)
	}

	if succeeded(t, deleteSite(t, engine, digest)) {
		t.Fatal("a site that a node serves was deleted")
	}
	if !strings.Contains(deleteSite(t, engine, digest).Body.String(), "edge") {
		t.Fatal("the refusal does not say which node serves the site")
	}
	if sites := listedSites(t, engine); len(sites) != 1 {
		t.Fatalf("the list holds %+v after the refused delete, want the site", sites)
	}

	save := postForm(t, engine, "/panel/api/nodes/update/"+strconv.Itoa(id), url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"}, "enable": {"true"},
		"decoyMode": {"template"}, "decoyDomain": {"node.example"},
	})
	if !succeeded(t, save) {
		t.Fatalf("the save answered %s", save.Body.String())
	}
	if !succeeded(t, deleteSite(t, engine, digest)) {
		t.Fatal("the site was not deleted once no node served it")
	}
	if sites := listedSites(t, engine); len(sites) != 0 {
		t.Fatalf("the list holds %+v after the delete, want none", sites)
	}
}

func TestNodeControllerRefusesWhatIsNotAnUploadedSite(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	for name, w := range map[string]*httptest.ResponseRecorder{
		"a file that is no zip":    uploadSite(t, engine, "site", "x.zip", []byte("not a zip")),
		"a zip with no index page": uploadSite(t, engine, "site", "x.zip", siteArchive(t, map[string]string{"about.html": "x"})),
		"another form field":       uploadSite(t, engine, "file", "x.zip", siteArchive(t, map[string]string{"index.html": "x"})),
		"no file at all":           postForm(t, engine, "/panel/api/nodes/decoySites/upload", url.Values{"site": {"text"}}),
	} {
		if succeeded(t, w) {
			t.Errorf("%s was kept: %s", name, w.Body.String())
		}
	}
	if sites := listedSites(t, engine); len(sites) != 0 {
		t.Fatalf("the list holds %+v after the refusals, want none", sites)
	}
	if succeeded(t, deleteSite(t, engine, "not-a-digest")) {
		t.Fatal("a delete of a name that is no digest succeeded")
	}
}

func TestNodeControllerRefusesADecoyOfASiteThePanelDoesNotHold(t *testing.T) {
	engine := newNodeCredentialTestEngine(t)
	w := postForm(t, engine, "/panel/api/nodes/addAgent", url.Values{
		"name": {"edge"}, "address": {"203.0.113.7"}, "port": {"8443"},
		"decoyMode": {"upload"}, "decoySite": {strings.Repeat("a", 64)}, "decoyDomain": {"node.example"},
	})
	if succeeded(t, w) {
		t.Fatalf("a node was added with a site the panel does not hold: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "upload it again") {
		t.Fatalf("the refusal = %s, want it to say that the site has to be uploaded", w.Body.String())
	}
}
