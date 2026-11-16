//go:build !windows

package agent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

func (s *serverFixture) putSite(digest string, body []byte) reply {
	s.t.Helper()
	return s.authed(http.MethodPut, agentproto.PathDecoySite+"?"+agentproto.QueryDecoySiteSha256+"="+digest, body)
}

func TestServerStoresADecoySiteAndServesItAsTheDecoy(t *testing.T) {
	s := newServerFixture(t)
	raw := siteZip(t, map[string]string{"index.html": "<html>home</html>", "404.html": "<html>gone</html>"})
	digest := digestOf(raw)

	stored := decode[agentproto.DecoySite](t, s.putSite(digest, raw), http.StatusOK)
	if stored.Sha256 != digest || stored.Size != int64(len(raw)) {
		t.Fatalf("answer = %+v, want the digest and the size of the zip", stored)
	}
	stats := decode[agentproto.Stats](t, s.authed(http.MethodGet, agentproto.PathStats, nil), http.StatusOK)
	if len(stats.DecoySites) != 1 || stats.DecoySites[0] != digest {
		t.Fatalf("stats decoySites = %v, want the site that was sent, which is how the master knows not to send it again", stats.DecoySites)
	}

	body := `{"mode":"upload","site":"` + digest + `","domain":"node.example"}`
	view := decode[agentproto.Decoy](t, s.putDecoy(body), http.StatusOK)
	if view.Spec.Site != digest || view.Revision != agentproto.DecoyRevision(view.Spec) {
		t.Fatalf("decoy = %+v, want the one that names the site", view)
	}

	unknown := `{"mode":"upload","site":"` + strings.Repeat("a", 64) + `","domain":"node.example"}`
	if got := s.putDecoy(unknown); got.status != http.StatusBadRequest || !strings.Contains(got.body, "send it first") {
		t.Fatalf("a site the agent does not hold = %d %s, want a 400 that asks for it first", got.status, got.body)
	}
}

func TestServerRefusesADecoySiteThatIsWrong(t *testing.T) {
	s := newServerFixture(t)
	raw := siteZip(t, map[string]string{"index.html": "<html>home</html>"})
	noIndex := siteZip(t, map[string]string{"about.html": "<html>about</html>"})
	for name, got := range map[string]reply{
		"no digest":                     s.authed(http.MethodPut, agentproto.PathDecoySite, raw),
		"a digest that is not hex":      s.putSite(strings.Repeat("z", 64), raw),
		"a digest in capitals":          s.putSite(strings.ToUpper(digestOf(raw)), raw),
		"a body that is not its digest": s.putSite(digestOf([]byte("another")), raw),
		"a body that is no zip":         s.putSite(digestOf([]byte("not a zip")), []byte("not a zip")),
		"an empty body":                 s.putSite(digestOf(nil), nil),
		"a zip with no index page":      s.putSite(digestOf(noIndex), noIndex),
	} {
		if got.status != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", name, got.status, got.body)
		}
	}
	stats := decode[agentproto.Stats](t, s.authed(http.MethodGet, agentproto.PathStats, nil), http.StatusOK)
	if len(stats.DecoySites) != 0 {
		t.Fatalf("stats decoySites = %v after refusals, want none", stats.DecoySites)
	}
}

// Through the handler directly: a body past the cap over a real connection makes the client race the early reply.
func TestServerRefusesADecoySiteBodyPastTheCap(t *testing.T) {
	previous := maxSiteBytes
	maxSiteBytes = 1 << 10
	t.Cleanup(func() { maxSiteBytes = previous })
	s := newServerFixture(t)

	body := strings.Repeat("x", int(maxSiteBytes)+1)
	req := httptest.NewRequest(http.MethodPut, agentproto.PathDecoySite+"?"+agentproto.QueryDecoySiteSha256+"="+digestOf([]byte(body)), strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.bundle.Secret)
	rec := httptest.NewRecorder()
	s.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("a body past the cap = %d %s, want 413", rec.Code, rec.Body.String())
	}
	if got := s.server.decoy.Sites(); len(got) != 0 {
		t.Fatalf("Sites() = %v, want none stored", got)
	}
}

func TestServerAnswersTheDecoySitePathLikeTheOthers(t *testing.T) {
	s := newServerFixture(t)
	raw := siteZip(t, map[string]string{"index.html": "<html>home</html>"})
	digest := digestOf(raw)
	want := reply{status: http.StatusNotFound, contentType: "text/plain; charset=utf-8", body: "404 page not found\n"}
	path := agentproto.PathDecoySite + "?" + agentproto.QueryDecoySiteSha256 + "=" + digest
	for name, got := range map[string]reply{
		"wrong method": s.authed(http.MethodGet, path, nil),
		"no secret":    s.do(http.MethodPut, path, raw, ""),
		"wrong secret": s.do(http.MethodPut, path, raw, "Bearer "+strings.Repeat("A", 43)),
	} {
		if got != want {
			t.Fatalf("%s: %+v, want the bare 404 %+v", name, got, want)
		}
	}
	if got := s.server.decoy.Sites(); len(got) != 0 {
		t.Fatalf("Sites() = %v, want a refused request to store nothing", got)
	}
}
