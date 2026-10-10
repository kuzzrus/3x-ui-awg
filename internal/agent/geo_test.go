//go:build !windows

package agent

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func digestOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func gzipped(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// putGeo sends a geo file the way the master does: name and digest in the query, the body as is
// or encoded.
func (s *serverFixture) putGeo(name, digest string, body []byte, encoding string) reply {
	s.t.Helper()
	query := url.Values{agentproto.QueryGeoName: {name}, agentproto.QueryGeoSha256: {digest}}
	req, err := http.NewRequest(http.MethodPut, s.url+agentproto.PathGeo+"?"+query.Encode(), bytes.NewReader(body))
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.bundle.Secret)
	if encoding != "" {
		req.Header.Set("Content-Encoding", encoding)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		s.t.Fatalf("PUT %s: %v", agentproto.PathGeo, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.t.Fatal(err)
	}
	return reply{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), body: string(raw)}
}

func (s *serverFixture) leftovers() []string {
	s.t.Helper()
	matches, err := filepath.Glob(filepath.Join(s.binDir, ".geo-*"))
	if err != nil {
		s.t.Fatal(err)
	}
	return matches
}

func (s *serverFixture) write(name, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.binDir, name), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *serverFixture) read(name string) string {
	s.t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.binDir, name))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(raw)
}

func TestServerListsTheGeoFilesTheCoreReads(t *testing.T) {
	s := newServerFixture(t)
	s.write("geoip.dat", "12345")
	s.write("geosite_runet.dat", "1234567")
	s.write("notes.txt", "not a geo file")
	s.write("bad name.dat", "no")
	if err := os.Mkdir(filepath.Join(s.binDir, "folder.dat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.binDir, "geoip.dat"), filepath.Join(s.binDir, "linked.dat")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.binDir, "missing"), filepath.Join(s.binDir, "dangling.dat")); err != nil {
		t.Fatal(err)
	}

	got := decode[agentproto.GeoFiles](t, s.authed(http.MethodGet, agentproto.PathGeo, nil), http.StatusOK)
	want := []agentproto.GeoFile{{Name: "geoip.dat", Size: 5}, {Name: "geosite_runet.dat", Size: 7}, {Name: "linked.dat", Size: 5}}
	if len(got.Files) != len(want) {
		t.Fatalf("files = %+v, want %+v", got.Files, want)
	}
	for i := range want {
		if got.Files[i] != want[i] {
			t.Fatalf("files = %+v, want %+v", got.Files, want)
		}
	}
}

func TestServerListsNoGeoFilesAsAnEmptyList(t *testing.T) {
	s := newServerFixture(t)

	r := s.authed(http.MethodGet, agentproto.PathGeo, nil)
	if r.status != http.StatusOK || !strings.Contains(r.body, `"files":[]`) {
		t.Fatalf("answer = %d %s, want an empty list and not null", r.status, r.body)
	}
}

func TestServerStoresAGeoFileWhoseDigestMatches(t *testing.T) {
	content := []byte(strings.Repeat("geo data ", 1000))
	for _, encoding := range []string{"", "gzip"} {
		t.Run("encoding="+encoding, func(t *testing.T) {
			s := newServerFixture(t)
			body := content
			if encoding == "gzip" {
				body = gzipped(t, content)
			}

			got := decode[agentproto.GeoFile](t, s.putGeo("geoip_x.dat", digestOf(content), body, encoding), http.StatusOK)
			if got.Name != "geoip_x.dat" || got.Size != int64(len(content)) {
				t.Fatalf("answer = %+v, want the stored name and size", got)
			}
			if s.read("geoip_x.dat") != string(content) {
				t.Fatal("the stored file is not what was sent")
			}
			info, err := os.Stat(filepath.Join(s.binDir, "geoip_x.dat"))
			if err != nil || info.Mode().Perm() != 0o644 {
				t.Fatalf("stored file mode = %v (%v), want 0644: the core reads it", info.Mode().Perm(), err)
			}
			if left := s.leftovers(); len(left) != 0 {
				t.Fatalf("temporary files left behind: %v", left)
			}
			listed := decode[agentproto.GeoFiles](t, s.authed(http.MethodGet, agentproto.PathGeo, nil), http.StatusOK)
			if len(listed.Files) != 1 || listed.Files[0].Name != "geoip_x.dat" {
				t.Fatalf("listed = %+v, want the stored file", listed.Files)
			}
		})
	}
}

func TestServerReplacesAGeoFileWholeAndNotWhenItsDigestIsWrong(t *testing.T) {
	s := newServerFixture(t)
	s.write("geoip.dat", "old content")
	fresh := []byte("fresh content")

	r := s.putGeo("geoip.dat", digestOf([]byte("something else")), fresh, "")
	if r.status != http.StatusBadRequest || !strings.Contains(r.body, "sha256") {
		t.Fatalf("answer = %d %s, want a refusal that names the digest", r.status, r.body)
	}
	if s.read("geoip.dat") != "old content" {
		t.Fatal("a file whose digest did not match replaced the one the core reads")
	}
	if left := s.leftovers(); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}

	decode[agentproto.GeoFile](t, s.putGeo("geoip.dat", digestOf(fresh), fresh, ""), http.StatusOK)
	if s.read("geoip.dat") != "fresh content" {
		t.Fatal("the matching file did not replace the old one")
	}
}

func TestServerRefusesWhatItCannotStoreAsAGeoFile(t *testing.T) {
	content := []byte("some geo data")
	good := digestOf(content)
	tests := []struct {
		name     string
		file     string
		digest   string
		body     []byte
		encoding string
		status   int
	}{
		{"a path for a name", "../geoip.dat", good, content, "", http.StatusBadRequest},
		{"a name that is no geo file", "config.json", good, content, "", http.StatusBadRequest},
		{"no name", "", good, content, "", http.StatusBadRequest},
		{"no digest", "geoip.dat", "", content, "", http.StatusBadRequest},
		{"a digest that is no digest", "geoip.dat", "not-hex", content, "", http.StatusBadRequest},
		{"a short digest", "geoip.dat", good[:32], content, "", http.StatusBadRequest},
		{"an empty body", "geoip.dat", digestOf(nil), nil, "", http.StatusBadRequest},
		{"a body that is no gzip", "geoip.dat", good, content, "gzip", http.StatusBadRequest},
		{"an encoding it does not read", "geoip.dat", good, content, "br", http.StatusUnsupportedMediaType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newServerFixture(t)
			s.write("geoip.dat", "untouched")

			r := s.putGeo(tt.file, tt.digest, tt.body, tt.encoding)
			if r.status != tt.status {
				t.Fatalf("status = %d (%s), want %d", r.status, r.body, tt.status)
			}
			if s.read("geoip.dat") != "untouched" {
				t.Fatal("a refused upload changed a geo file")
			}
			if left := s.leftovers(); len(left) != 0 {
				t.Fatalf("temporary files left behind: %v", left)
			}
			if _, err := os.Stat(filepath.Join(s.binDir, "..", "geoip.dat")); err == nil && tt.name == "a path for a name" {
				t.Fatal("a name with a path wrote outside the asset folder")
			}
		})
	}
}

func TestServerCapsTheSizeOfAGeoFileWhateverTheEncoding(t *testing.T) {
	previous := maxGeoBytes
	maxGeoBytes = 1024
	t.Cleanup(func() { maxGeoBytes = previous })
	tooBig := bytes.Repeat([]byte("a"), int(maxGeoBytes)+1)
	fits := bytes.Repeat([]byte("a"), int(maxGeoBytes))

	s := newServerFixture(t)
	if r := s.putGeo("geoip.dat", digestOf(tooBig), tooBig, ""); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("plain body over the cap: status = %d (%s), want 413", r.status, r.body)
	}
	// A few bytes of gzip stand for a body far above the cap once unpacked.
	if r := s.putGeo("geoip.dat", digestOf(tooBig), gzipped(t, tooBig), "gzip"); r.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("gzip body over the cap: status = %d (%s), want 413", r.status, r.body)
	}
	if left := s.leftovers(); len(left) != 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
	if _, err := os.Stat(filepath.Join(s.binDir, "geoip.dat")); err == nil {
		t.Fatal("a file over the cap was stored")
	}
	decode[agentproto.GeoFile](t, s.putGeo("geoip.dat", digestOf(fits), fits, ""), http.StatusOK)
}

func TestServerKeepsTheGeoRoutesBehindTheSecretAndTheirMethods(t *testing.T) {
	s := newServerFixture(t)
	content := []byte("some geo data")
	query := "?" + agentproto.QueryGeoName + "=geoip.dat&" + agentproto.QueryGeoSha256 + "=" + digestOf(content)

	for _, tt := range []struct{ method, path, authorization string }{
		{http.MethodGet, agentproto.PathGeo, ""},
		{http.MethodGet, agentproto.PathGeo, "Bearer wrong"},
		{http.MethodPut, agentproto.PathGeo + query, ""},
		{http.MethodPut, agentproto.PathGeo + query, "Bearer wrong"},
		{http.MethodPost, agentproto.PathGeo, "Bearer " + s.bundle.Secret},
		{http.MethodDelete, agentproto.PathGeo + query, "Bearer " + s.bundle.Secret},
	} {
		r := s.do(tt.method, tt.path, content, tt.authorization)
		if r.status != http.StatusNotFound {
			t.Errorf("%s %s with %q: status = %d, want the bare 404", tt.method, tt.path, tt.authorization, r.status)
		}
	}
	if _, err := os.Stat(filepath.Join(s.binDir, "geoip.dat")); err == nil {
		t.Fatal("a request the server refused stored a file")
	}
}
