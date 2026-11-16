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

// The panel's server stops reading a request after 30 seconds, which a large site on a slow uplink outlasts. The same stop is set
// here at a fraction of a second, with a body that waits in the middle, and the upload route must still keep the site.
func TestRouterTakesADecoySiteThatArrivesSlowly(t *testing.T) {
	n := startContractNodeWith(t, func(s *http.Server) {
		s.ReadTimeout, s.WriteTimeout = 300*time.Millisecond, 300*time.Millisecond
	})
	token := "decoy-site-slow"
	if err := database.GetDB().Create(&model.ApiToken{
		Name: "decoy-site-slow", Token: crypto.HashTokenSHA256(token), Enabled: true, Scope: model.ApiScopeAdmin,
	}).Error; err != nil {
		t.Fatal(err)
	}
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
	contentType, form := multipartOf(t, "site", "slow.zip", site.Bytes())
	body := form.Bytes()

	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write(body[:len(body)/2])
		time.Sleep(900 * time.Millisecond)
		_, _ = writer.Write(body[len(body)/2:])
		_ = writer.Close()
	}()
	req, err := http.NewRequest(http.MethodPost, n.srv.URL+"/panel/api/nodes/decoySites/upload", reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("the upload that waited in the middle was cut off: %v", err)
	}
	defer resp.Body.Close()
	var got reply
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil || !got.Success {
		t.Fatalf("the upload that waited in the middle answered %d %+v, %v, want it kept", resp.StatusCode, got, err)
	}
}
