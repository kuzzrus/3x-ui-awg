package web

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

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
