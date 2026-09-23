package tproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// serverPort extracts the loopback port a httptest server bound to, so
// scrapeActiveConnections can rebuild the same http://127.0.0.1:<port>/stats
// URL. Mirrors internal/mtproto's identical helper.
func serverPort(t *testing.T, srv *httptest.Server) int {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return port
}

// fakeMtproxyStats is a representative slice of the real tab-separated body
// mtfront_prepare_stats emits, in the same order, with the same neighbouring
// keys -- proving the parser picks the right line, not just any line
// containing digits.
func fakeMtproxyStats(activeConnections int) string {
	return "config_filename\t/path/to/config\n" +
		"workers\t0\n" +
		"queries_get\t42\n" +
		"total_ready_targets\t1\n" +
		"total_allocated_targets\t1\n" +
		"total_declared_targets\t1\n" +
		"total_inactive_targets\t0\n" +
		"total_connections\t" + strconv.Itoa(activeConnections) + "\n" +
		"total_encrypted_connections\t" + strconv.Itoa(activeConnections) + "\n" +
		"total_allocated_connections\t3\n" +
		"version\tmtproto-proxy-0.01 compiled at ...\n"
}

func statsServer(t *testing.T, path, body string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return serverPort(t, srv)
}

func TestScrapeActiveConnections(t *testing.T) {
	port := statsServer(t, "/stats", fakeMtproxyStats(3))

	n, ok := scrapeActiveConnections(port)
	if !ok {
		t.Fatal("scrapeActiveConnections should succeed against a valid /stats endpoint")
	}
	if n != 3 {
		t.Fatalf("active connections = %d, want 3", n)
	}
}

func TestScrapeActiveConnectionsZero(t *testing.T) {
	port := statsServer(t, "/stats", fakeMtproxyStats(0))

	n, ok := scrapeActiveConnections(port)
	if !ok {
		t.Fatal("scrapeActiveConnections should succeed even when the count is zero")
	}
	if n != 0 {
		t.Fatalf("active connections = %d, want 0", n)
	}
}

func TestScrapeActiveConnectionsMissingKey(t *testing.T) {
	port := statsServer(t, "/stats", "config_filename\t/path/to/config\nworkers\t0\n")

	if _, ok := scrapeActiveConnections(port); ok {
		t.Fatal("scrapeActiveConnections must report ok=false when total_connections is absent")
	}
}

func TestScrapeActiveConnectionsMalformedValue(t *testing.T) {
	port := statsServer(t, "/stats", "total_connections\tnot-a-number\n")

	if _, ok := scrapeActiveConnections(port); ok {
		t.Fatal("scrapeActiveConnections must report ok=false on a non-numeric value")
	}
}

func TestScrapeActiveConnectionsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, fakeMtproxyStats(1))
	}))
	port := serverPort(t, srv)
	srv.Close() // closed before the call under test: a real "connection refused" port

	if _, ok := scrapeActiveConnections(port); ok {
		t.Fatal("scrapeActiveConnections must report ok=false when the endpoint is unreachable")
	}
}

func TestScrapeActiveConnectionsNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	if _, ok := scrapeActiveConnections(serverPort(t, srv)); ok {
		t.Fatal("scrapeActiveConnections must report ok=false on a non-200 response")
	}
}
