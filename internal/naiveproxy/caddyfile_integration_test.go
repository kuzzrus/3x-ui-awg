package naiveproxy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// writeSelfSignedCert writes a throwaway cert/key pair -- "caddy validate"
// provisions the full config, so a placeholder path fails differently here.
func writeSelfSignedCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "naiveproxy-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// canReachGitHub is checked before Install so a real Install failure fails
// the test, instead of being swallowed as "assume no network".
func canReachGitHub(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// TestRenderCaddyfileValidatesAgainstTheRealBinary runs "caddy validate" on
// renderCaddyfile's output -- gated behind XUI_NAIVE_E2E=1 (downloads ~12 MiB).
func TestRenderCaddyfileValidatesAgainstTheRealBinary(t *testing.T) {
	if os.Getenv("XUI_NAIVE_E2E") == "" {
		t.Skip("set XUI_NAIVE_E2E=1 to run (downloads the real Caddy release)")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the pinned Caddy release only runs on linux/amd64")
	}
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if !canReachGitHub(ctx) {
		t.Skip("no network reachability to github.com in this environment")
	}
	if err := Install(ctx, http.DefaultClient); err != nil {
		t.Fatalf("Install (network was reachable): %v", err)
	}

	inst := testInstance()
	inst.CertFile, inst.KeyFile = writeSelfSignedCert(t)
	inst.RouteThroughXray = true
	inst.XrayRoutePort = 41200
	caddyfile, err := renderCaddyfile(inst)
	if err != nil {
		t.Fatalf("renderCaddyfile: %v", err)
	}

	cfgPath := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(cfgPath, []byte(caddyfile), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, BinPath(), "validate", "--config", cfgPath, "--adapter", "caddyfile")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("caddy validate rejected the rendered Caddyfile: %v\n--- output ---\n%s\n--- Caddyfile ---\n%s", err, out, caddyfile)
	}
}

// startRealNaive installs the pinned Caddy and runs it on a self-signed cert with one
// client, camo-user/camo-pass, whose tunnels feed access. Gated like the test above.
func startRealNaive(t *testing.T) (ctx context.Context, inst Instance, access *meter, proc *Process) {
	t.Helper()
	if os.Getenv("XUI_NAIVE_E2E") == "" {
		t.Skip("set XUI_NAIVE_E2E=1 to run (downloads the real Caddy release)")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("the pinned Caddy release only runs on linux/amd64")
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not found in PATH")
	}
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	if !canReachGitHub(ctx) {
		t.Skip("no network reachability to github.com in this environment")
	}
	if err := Install(ctx, http.DefaultClient); err != nil {
		t.Fatalf("Install (network was reachable): %v", err)
	}

	inst = testInstance()
	inst.Id = 1
	inst.ListenAddr = fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	inst.CertFile, inst.KeyFile = writeSelfSignedCert(t)
	inst.Clients = []Client{{Email: "camo-user", Username: "camo-user", Password: "camo-pass"}}

	writeDecoyContent(inst)
	caddyfile, err := renderCaddyfile(inst)
	if err != nil {
		t.Fatalf("renderCaddyfile: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(cfgPath, []byte(caddyfile), 0o600); err != nil {
		t.Fatal(err)
	}

	access = newMeter("inbound-1")
	proc = newProcess(cfgPath, inst.ListenAddr, "e2e-test", access)
	if err := proc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = proc.Stop() })
	if err := proc.WaitReady(); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	return ctx, inst, access, proc
}

// TestNaiveProxyMetersTunnelsAgainstTheRealBinary pins the access-log shape
// accounting.go parses to what the pinned Caddy build really writes.
func TestNaiveProxyMetersTunnelsAgainstTheRealBinary(t *testing.T) {
	ctx, inst, access, _ := startRealNaive(t)

	proxyURL := fmt.Sprintf("https://camo-user:camo-pass@%s", inst.ListenAddr)
	out, err := exec.CommandContext(ctx, "curl", "-s", "-x", proxyURL, "--proxy-insecure", "-o", "/dev/null", "-w", "%{size_download}", "https://example.com/").CombinedOutput()
	if err != nil {
		t.Fatalf("curl: %v\n%s", err, out)
	}
	downloaded, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || downloaded <= 0 {
		t.Fatalf("curl reported %q downloaded, want a positive byte count (err %v)", out, err)
	}

	// The tunnel is metered as it closes, which is right after curl exits.
	var got []Traffic
	waitUntil(t, "the closed tunnel to be metered", func() bool {
		got = append(got, access.drain()...)
		return len(got) > 0
	})
	if len(got) != 1 || got[0].Email != "camo-user" || got[0].Tag != "inbound-1" {
		t.Fatalf("metered %+v, want exactly one record for camo-user under inbound-1", got)
	}
	// The tunnel carries the client's whole TLS session, so it is at least the page.
	if got[0].Up <= 0 || got[0].Down < downloaded {
		t.Errorf("metered up=%d down=%d, want up > 0 and down >= the %d body bytes curl received", got[0].Up, got[0].Down, downloaded)
	}

	t.Run("a wrong password meters nothing", func(t *testing.T) {
		bad := fmt.Sprintf("https://camo-user:wrong@%s", inst.ListenAddr)
		_, _ = exec.CommandContext(ctx, "curl", "-s", "-x", bad, "--proxy-insecure", "-o", "/dev/null", "https://example.com/").CombinedOutput()
		time.Sleep(500 * time.Millisecond)
		if extra := access.drain(); len(extra) != 0 {
			t.Errorf("metered %+v for a failed login", extra)
		}
	})
}

// resetServer resets every connection to this host's public address: Caddy logs an error only
// for a tunnel torn down mid-flight, and forward_proxy refuses private targets, so it skips without one.
func resetServer(t *testing.T) (host string, port int) {
	t.Helper()
	probe, err := net.Dial("udp", "1.1.1.1:53") // sends nothing, only picks the route
	if err != nil {
		t.Skipf("no outbound route: %v", err)
	}
	ip := probe.LocalAddr().(*net.UDPAddr).IP
	probe.Close()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() {
		t.Skipf("outbound address %s is not public, forward_proxy would refuse it as a target", ip)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.(*net.TCPConn).SetLinger(0)
			c.Close()
		}
	}()
	return ip.String(), ln.Addr().(*net.TCPAddr).Port
}

// TestNaiveProxyErrorLogsOmitTheRequestAgainstTheRealBinary pins the global log filter: the
// error Caddy logs for a tunnel its target resets must not carry the request.
func TestNaiveProxyErrorLogsOmitTheRequestAgainstTheRealBinary(t *testing.T) {
	ctx, inst, _, proc := startRealNaive(t)
	host, port := resetServer(t)

	proxyURL := fmt.Sprintf("https://camo-user:camo-pass@%s", inst.ListenAddr)
	target := fmt.Sprintf("https://%s:%d/", host, port)
	// Whether Caddy sees a reset as an error or as a plain close is a race, so retry until it logs one.
	waitUntil(t, "Caddy to log a reset tunnel", func() bool {
		_, _ = exec.CommandContext(ctx, "curl", "-s", "--proxy-http2", "-x", proxyURL, "--proxy-insecure", target).CombinedOutput()
		return strings.Contains(proc.logWriter.LastLine(), `"level":"error"`)
	})
	if line := proc.logWriter.LastLine(); strings.Contains(line, `"request"`) {
		t.Errorf("an error line still carries the request:\n%s", line)
	}
}

// TestNaiveProxyCamouflageAgainstTheRealBinary confirms an ordinary visitor
// sees the decoy, not a proxy-revealing 407. Gated like the test above.
func TestNaiveProxyCamouflageAgainstTheRealBinary(t *testing.T) {
	ctx, inst, _, _ := startRealNaive(t)

	t.Run("valid credentials still tunnel real traffic", func(t *testing.T) {
		// A real external site: forward_proxy's default ACL denies CONNECT
		// to 127.0.0.0/8 and other private ranges, confirmed live.
		proxyURL := fmt.Sprintf("https://camo-user:camo-pass@%s", inst.ListenAddr)
		out, err := exec.CommandContext(ctx, "curl", "-s", "-x", proxyURL, "--proxy-insecure", "https://example.com/").CombinedOutput()
		if err != nil {
			t.Fatalf("curl: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "Example Domain") {
			t.Errorf("expected example.com's real content through the tunnel, got:\n%s", out)
		}
	})

	t.Run("an ordinary visitor sees the decoy, not a proxy challenge", func(t *testing.T) {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
		resp, err := client.Get("https://" + inst.ListenAddr + "/")
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusProxyAuthRequired {
			t.Fatalf("ordinary visitor got %d Proxy Authentication Required -- probe_resistance/route{} not doing its job", resp.StatusCode)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if !bytes.Contains(body, []byte("<html")) {
			t.Errorf("expected the rendered decoy HTML, got:\n%s", body)
		}
	})

	t.Run("wrong credentials do not leak a proxy-specific challenge", func(t *testing.T) {
		proxyURL := fmt.Sprintf("https://wrong:creds@%s", inst.ListenAddr)
		out, _ := exec.CommandContext(ctx, "curl", "-s", "-v", "-x", proxyURL, "--proxy-insecure", "https://example.com/").CombinedOutput()
		if strings.Contains(string(out), "407 Proxy Authentication Required") {
			t.Errorf("wrong credentials got a bare 407 Proxy Authentication Required -- the exact signal probe_resistance exists to hide:\n%s", out)
		}
	})
}
