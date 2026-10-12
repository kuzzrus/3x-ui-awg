//go:build !windows

package agent

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
)

// writeDecoyCert makes a self-signed certificate for a name, as a node would hold files for.
func writeDecoyCert(t *testing.T, name string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		DNSNames:     []string{name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func freeLoopbackPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func probe(t *testing.T, port int, serverName, path string) (int, string, error) {
	t.Helper()
	client := &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{ServerName: serverName, InsecureSkipVerify: true}}, //nolint:gosec // a self-signed certificate of the test
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("https://127.0.0.1:" + strconv.Itoa(port) + path)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

// The agent against the real listener: what the REALITY fallback of the core is handed when it points here.
func TestDecoyHostServesTheChosenPageOverTLSAndOnlyForItsName(t *testing.T) {
	certFile, keyFile := writeDecoyCert(t, "node.example")
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newDecoyHost(state, frontProxyRunner{}, "node-seed", t.TempDir())
	t.Cleanup(host.stop)
	port := freeLoopbackPort(t)
	spec := agentproto.DecoySpec{
		Mode: agentproto.DecoyTemplate, Template: "parked", Domain: "node.example",
		Cert: agentproto.DecoyCertManual, CertFile: certFile, KeyFile: keyFile, Port: port,
	}

	if _, err := host.Set(spec); err != nil {
		t.Fatal(err)
	}
	host.reconcile()
	if got := host.State(); got.State != agentproto.DecoyStateRunning || got.Cert != agentproto.DecoyCertObtained || got.CertNotAfter == 0 {
		t.Fatalf("state = %+v, want running with the certificate of the files", got)
	}
	for _, path := range []string{"/", "/p/panel/inbounds"} {
		if _, body, err := probe(t, port, "node.example", path); err != nil || !strings.Contains(body, "Здесь") {
			t.Fatalf("GET %s = %q, %v; want the page that was chosen", path, body, err)
		}
	}
	if _, _, err := probe(t, port, "other.example", "/"); err == nil {
		t.Fatal("a probe that asked for another name was given a certificate")
	}

	// Another page is swapped into the running listener.
	spec.Template = "tetris"
	if _, err := host.Set(spec); err != nil {
		t.Fatal(err)
	}
	host.reconcile()
	if _, body, err := probe(t, port, "node.example", "/"); err != nil || strings.Contains(body, "Здесь") {
		t.Fatalf("GET / after the change = %q, %v; want the other page", body, err)
	}

	// A proxy answers with what the site behind it says, which a test keeps on the loopback.
	host.privateProxy = true
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("the site behind")) }))
	t.Cleanup(upstream.Close)
	spec.Mode, spec.Template, spec.ProxyURL = agentproto.DecoyProxy, "", upstream.URL
	if _, err := host.Set(spec); err != nil {
		t.Fatal(err)
	}
	host.reconcile()
	if _, body, err := probe(t, port, "node.example", "/"); err != nil || body != "the site behind" {
		t.Fatalf("GET / through the proxy = %q, %v", body, err)
	}

	// Taking the decoy off closes the port.
	if _, err := host.Set(agentproto.DecoySpec{}); err != nil {
		t.Fatal(err)
	}
	host.reconcile()
	if _, _, err := probe(t, port, "node.example", "/"); err == nil {
		t.Fatal("the port still answers after the decoy was taken off")
	}
}
