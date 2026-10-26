package psiphon

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// socksProxy is the smallest SOCKS5 server that carries a CONNECT, which is all the probe asks of a tunnel.
// With refuse set it takes the connection and drops it, as Psiphon does for a request before its tunnel is up.
func socksProxy(t *testing.T, refuse bool) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if refuse {
					return
				}
				serveSocksConnect(conn)
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port
}

func serveSocksConnect(conn net.Conn) {
	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil || head[0] != 5 {
		return
	}
	if _, err := io.CopyN(io.Discard, conn, int64(head[1])); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 0}); err != nil { // no authentication
		return
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil || req[1] != 1 {
		return
	}
	var host string
	switch req[3] {
	case 1:
		ip := make([]byte, 4)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 3:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return
		}
		name := make([]byte, size[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return
		}
		host = string(name)
	default:
		return
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		return
	}
	target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(portBytes)))))
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(target, conn) }()
	_, _ = io.Copy(conn, target)
}

// withProbeTargets points the probe at servers of the test, and gives a tunnel that does not answer little time to.
func withProbeTargets(t *testing.T, urls []string, exit string) {
	t.Helper()
	oldURLs, oldExit := probeURLs, exitURL
	probeURLs, exitURL = urls, exit
	t.Cleanup(func() { probeURLs, exitURL = oldURLs, oldExit })
}

func probeServers(t *testing.T) (generate204, exitInfo *httptest.Server) {
	t.Helper()
	generate204 = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	exitInfo = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ip":"203.0.113.9","country":"DE"}`)
	}))
	t.Cleanup(generate204.Close)
	t.Cleanup(exitInfo.Close)
	return generate204, exitInfo
}

func TestProbeReportsTheLatencyAndTheExitOfATunnel(t *testing.T) {
	port := socksProxy(t, false)
	ok, exit := probeServers(t)
	withProbeTargets(t, []string{ok.URL}, exit.URL)

	got := Probe(context.Background(), port)
	if got.Error != "" || got.IP != "203.0.113.9" || got.Country != "DE" {
		t.Fatalf("probe = %+v, want the exit 203.0.113.9 in DE and no error", got)
	}
	if got.LatencyMs < 0 || got.LatencyMs > 5000 {
		t.Fatalf("latency = %d ms, not a plausible round trip", got.LatencyMs)
	}
}

func TestProbeAsksTheSecondAddressWhenTheFirstDoesNotAnswer(t *testing.T) {
	port := socksProxy(t, false)
	ok, exit := probeServers(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close() // nothing listens there any more
	withProbeTargets(t, []string{deadURL, ok.URL}, exit.URL)

	got := Probe(context.Background(), port)
	if got.Error != "" || got.IP == "" {
		t.Fatalf("probe = %+v, want the second address to have answered", got)
	}
}

func TestProbeSaysSoWhenTheTunnelDoesNotAnswer(t *testing.T) {
	port := socksProxy(t, true)
	ok, exit := probeServers(t)
	withProbeTargets(t, []string{ok.URL}, exit.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	got := Probe(ctx, port)
	if !strings.Contains(got.Error, "no answer through the tunnel") || got.IP != "" || got.LatencyMs != 0 {
		t.Fatalf("probe = %+v, want the missing answer as the error and nothing else", got)
	}
}

func TestProbeKeepsTheLatencyWhenTheExitCannotBeLookedUp(t *testing.T) {
	port := socksProxy(t, false)
	ok, _ := probeServers(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `not json`)
	}))
	t.Cleanup(broken.Close)
	withProbeTargets(t, []string{ok.URL}, broken.URL)

	got := Probe(context.Background(), port)
	if !strings.Contains(got.Error, "exit could not be looked up") || got.IP != "" {
		t.Fatalf("probe = %+v, want the failed lookup as the error", got)
	}
	if got.LatencyMs < 0 {
		t.Fatalf("the latency was lost with the lookup: %+v", got)
	}
}
