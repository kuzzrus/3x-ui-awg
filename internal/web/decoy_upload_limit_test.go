package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/crypto"
)

// reply is what the panel answers to an API call, as far as these tests look.
type reply struct {
	Success bool            `json:"success"`
	Msg     string          `json:"msg"`
	Obj     json.RawMessage `json:"obj"`
}

func postThroughTheRouter(t *testing.T, url, token, contentType string, body *bytes.Buffer) (int, reply) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out reply
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// siteOfAboutTwelveMiB is a zip of a site whose files do not compress, so it is well past the 10 MiB that the panel lets any other request carry.
func siteOfAboutTwelveMiB(t *testing.T) []byte {
	t.Helper()
	filler := make([]byte, 12<<20)
	if _, err := rand.Read(filler); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	index, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Write([]byte("<html>home</html>")); err != nil {
		t.Fatal(err)
	}
	picture, err := zw.CreateHeader(&zip.FileHeader{Name: "photo.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := picture.Write(filler); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func multipartOf(t *testing.T, field, filename string, content []byte) (string, *bytes.Buffer) {
	t.Helper()
	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return mw.FormDataContentType(), &form
}

// The page says a decoy site may be up to 64 MiB, so the router must not cut it off at the 10 MiB it allows other requests.
func TestRouterTakesADecoySiteLargerThanTheBodyCapOfOtherRoutes(t *testing.T) {
	n := startContractNode(t)
	token := "decoy-site-upload"
	if err := database.GetDB().Create(&model.ApiToken{
		Name: "decoy-site-upload", Token: crypto.HashTokenSHA256(token), Enabled: true, Scope: model.ApiScopeAdmin,
	}).Error; err != nil {
		t.Fatal(err)
	}
	site := siteOfAboutTwelveMiB(t)
	if len(site) <= 10<<20 {
		t.Fatalf("the site is %d bytes, want it past the cap of 10 MiB", len(site))
	}

	contentType, form := multipartOf(t, "site", "photos.zip", site)
	status, got := postThroughTheRouter(t, n.srv.URL+"/panel/api/nodes/decoySites/upload", token, contentType, form)
	if status != http.StatusOK || !got.Success {
		t.Fatalf("the upload of %d bytes answered %d %+v, want it kept", len(site), status, got)
	}
	var kept struct {
		Size int64 `json:"size"`
	}
	if err := json.Unmarshal(got.Obj, &kept); err != nil || kept.Size != int64(len(site)) {
		t.Fatalf("the site kept = %s, %v, want its %d bytes", got.Obj, err, len(site))
	}

	// Every other route still has the cap: the exemption is for the route that caps its own body.
	other, otherForm := multipartOf(t, "x", "x.bin", bytes.Repeat([]byte("x"), 11<<20))
	status, got = postThroughTheRouter(t, n.srv.URL+"/panel/api/nodes/add", token, other, otherForm)
	if got.Success || !strings.Contains(string(got.Obj), "too large") {
		t.Fatalf("a body of 11 MiB to another route answered %d %+v, want it refused for its size", status, got)
	}
}

// uploadSlowly posts a multipart file whose body waits in the middle, to the router served with a read and a write timeout of
// 300 ms, which stand in for the 30 seconds of the panel's server and an uplink too slow to beat them.
func uploadSlowly(t *testing.T, path, field, filename string, content []byte) (*http.Response, error) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	n := startContractNodeWith(t, func(s *http.Server) {
		s.ReadTimeout, s.WriteTimeout = 300*time.Millisecond, 300*time.Millisecond
	})
	token := "upload-slow"
	if err := database.GetDB().Create(&model.ApiToken{
		Name: "upload-slow", Token: crypto.HashTokenSHA256(token), Enabled: true, Scope: model.ApiScopeAdmin,
	}).Error; err != nil {
		t.Fatal(err)
	}
	contentType, form := multipartOf(t, field, filename, content)
	body := form.Bytes()

	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write(body[:len(body)/2])
		time.Sleep(900 * time.Millisecond)
		_, _ = writer.Write(body[len(body)/2:])
		_ = writer.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, n.srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	return http.DefaultClient.Do(req)
}

func smallSite(t *testing.T) []byte {
	t.Helper()
	var site bytes.Buffer
	zw := zip.NewWriter(&site)
	index, err := zw.Create("index.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := index.Write(bytes.Repeat([]byte("<html>home</html>"), 100)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return site.Bytes()
}

// The panel's server stops reading a request after 30 seconds, which a large site on a slow uplink outlasts. The upload routes
// that take large files lift that for themselves, and each of them is tried here, as each lifts it on its own.
func TestRouterTakesADecoySiteThatArrivesSlowly(t *testing.T) {
	for name, path := range map[string]string{
		"the decoy site of an agent node": "/panel/api/nodes/decoySites/upload",
		"the decoy site of the panel":     "/panel/api/xray/frontproxy/decoy/upload",
	} {
		t.Run(name, func(t *testing.T) {
			resp, err := uploadSlowly(t, path, "site", "slow.zip", smallSite(t))
			if err != nil {
				t.Fatalf("the upload that waited in the middle was cut off: %v", err)
			}
			defer resp.Body.Close()
			var got reply
			if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || !got.Success {
				t.Fatalf("the upload that waited in the middle answered %d %+v, %v, want it kept", resp.StatusCode, got, err)
			}
		})
	}
}

// A backup of the database is the largest file the panel takes. The one sent here is no database, so the restore refuses it, but
// the refusal has to come as an answer to a request that was read to its end and not as a connection cut off in the middle.
func TestRouterReadsADatabaseBackupThatArrivesSlowly(t *testing.T) {
	resp, err := uploadSlowly(t, "/panel/api/server/importDB", "db", "x-ui.db", bytes.Repeat([]byte("not a database "), 200))
	if err != nil {
		t.Fatalf("the backup that waited in the middle was cut off: %v", err)
	}
	defer resp.Body.Close()
	var got reply
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("the answer to the backup that waited in the middle is no reply: %v", err)
	}
	if got.Success || strings.Contains(strings.ToLower(got.Msg+string(got.Obj)), "timeout") {
		t.Fatalf("the backup that waited in the middle answered %+v, want a refusal of what it holds", got)
	}
}

// The decoy of the panel's own reverse proxy may be 64 MiB, as its page and its docs say, so it is let past the cap on other bodies.
func TestRouterTakesTheDecoySiteOfThePanelLargerThanTheBodyCapOfOtherRoutes(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	n := startContractNode(t)
	token := "panel-decoy-upload"
	if err := database.GetDB().Create(&model.ApiToken{
		Name: "panel-decoy-upload", Token: crypto.HashTokenSHA256(token), Enabled: true, Scope: model.ApiScopeAdmin,
	}).Error; err != nil {
		t.Fatal(err)
	}
	site := siteOfAboutTwelveMiB(t)
	contentType, form := multipartOf(t, "site", "photos.zip", site)
	status, got := postThroughTheRouter(t, n.srv.URL+"/panel/api/xray/frontproxy/decoy/upload", token, contentType, form)
	if status != http.StatusOK || !got.Success {
		t.Fatalf("the upload of %d bytes answered %d %+v, want it kept", len(site), status, got)
	}
	var kept struct {
		DecoyUploaded bool `json:"decoyUploaded"`
	}
	if err := json.Unmarshal(got.Obj, &kept); err != nil || !kept.DecoyUploaded {
		t.Fatalf("the status after the upload = %s, %v, want the decoy uploaded", got.Obj, err)
	}
}
