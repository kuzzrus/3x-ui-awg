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
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// TestNaiveProxyCamouflageAgainstTheRealBinary confirms an ordinary visitor
// sees the decoy, not a proxy-revealing 407. Gated like the test above.
func TestNaiveProxyCamouflageAgainstTheRealBinary(t *testing.T) {
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
	defer cancel()
	if !canReachGitHub(ctx) {
		t.Skip("no network reachability to github.com in this environment")
	}
	if err := Install(ctx, http.DefaultClient); err != nil {
		t.Fatalf("Install (network was reachable): %v", err)
	}

	inst := testInstance()
	inst.Id = 1
	inst.ListenAddr = fmt.Sprintf("127.0.0.1:%d", freeLoopbackPort(t))
	inst.CertFile, inst.KeyFile = writeSelfSignedCert(t)
	inst.Clients = []Client{{Email: "a@x", Username: "camo-user", Password: "camo-pass"}}

	writeDecoyContent(inst)
	caddyfile, err := renderCaddyfile(inst)
	if err != nil {
		t.Fatalf("renderCaddyfile: %v", err)
	}
	cfgPath := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(cfgPath, []byte(caddyfile), 0o600); err != nil {
		t.Fatal(err)
	}

	proc := newProcess(cfgPath, inst.ListenAddr, "camouflage-test")
	if err := proc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer proc.Stop()
	if err := proc.WaitReady(); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}

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
