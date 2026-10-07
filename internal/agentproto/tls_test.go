package agentproto

import (
	"bytes"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startAgent serves the bundle's TLS config and answers one "ping" per
// connection with "pong". It returns the listen address.
func startAgent(t *testing.T, b *Bundle) string {
	t.Helper()
	cfg, err := b.ServerTLSConfig()
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", cfg)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				ping := make([]byte, 4)
				if _, err := io.ReadFull(conn, ping); err != nil {
					return
				}
				_, _ = conn.Write([]byte("pong"))
			}()
		}
	}()
	return ln.Addr().String()
}

// ping dials addr and exchanges one message, failing on any handshake error.
func ping(addr string, cfg *tls.Config) error {
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", addr, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("ping")); err != nil {
		return err
	}
	pong := make([]byte, 4)
	if _, err := io.ReadFull(conn, pong); err != nil {
		return err
	}
	if string(pong) != "pong" {
		return fmt.Errorf("unexpected reply %q", pong)
	}
	return nil
}

func TestHandshake(t *testing.T) {
	b, fingerprint, err := NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	addr := startAgent(t, b)
	pin, err := hex.DecodeString(fingerprint)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	good, err := ClientTLSConfig(b.Secret, pin)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	otherSecret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	wrongSNI, err := ClientTLSConfig(otherSecret, pin)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	wrongPin, err := ClientTLSConfig(b.Secret, bytes.Repeat([]byte{1}, len(pin)))
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	upper := good.Clone()
	upper.ServerName = strings.ToUpper(good.ServerName)
	noSNI := good.Clone()
	noSNI.ServerName = "127.0.0.1"
	tls12 := good.Clone()
	tls12.MinVersion, tls12.MaxVersion = tls.VersionTLS12, tls.VersionTLS12

	tests := []struct {
		name string
		cfg  *tls.Config
		ok   bool
	}{
		{"derived sni and pinned certificate", good, true},
		{"sni is case-insensitive", upper, true},
		{"sni of another agent", wrongSNI, false},
		{"no sni", noSNI, false},
		{"certificate does not match the pin", wrongPin, false},
		{"tls 1.2 only", tls12, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ping(addr, tt.cfg)
			if (err == nil) != tt.ok {
				t.Fatalf("ping error = %v, want ok=%v", err, tt.ok)
			}
		})
	}
}

func TestWrongSNIGetsNoCertificate(t *testing.T) {
	b, fingerprint, err := NewBundle("127.0.0.1", 8443, time.Now())
	if err != nil {
		t.Fatalf("NewBundle: %v", err)
	}
	addr := startAgent(t, b)
	pin, err := hex.DecodeString(fingerprint)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	otherSecret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	cfg, err := ClientTLSConfig(otherSecret, pin)
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}

	var sawCertificate atomic.Bool
	inner := cfg.VerifyConnection
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		sawCertificate.Store(true)
		return inner(cs)
	}
	if err := ping(addr, cfg); err == nil {
		t.Fatal("handshake succeeded for the wrong server name")
	}
	if sawCertificate.Load() {
		t.Fatal("agent presented its certificate to a client with the wrong server name")
	}
}

func TestClientTLSConfigShape(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	cfg, err := ClientTLSConfig(secret, make([]byte, 32))
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	wantSNI, err := SNIFor(secret)
	if err != nil {
		t.Fatalf("SNIFor: %v", err)
	}
	if cfg.ServerName != wantSNI {
		t.Fatalf("ServerName = %q, want the derived %q", cfg.ServerName, wantSNI)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}
}

func TestClientTLSConfigRejects(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatalf("NewSecret: %v", err)
	}
	tests := []struct {
		name   string
		secret string
		pin    []byte
	}{
		{"bad secret", "short", make([]byte, 32)},
		{"no pin", secret, nil},
		{"short pin", secret, make([]byte, 31)},
		{"long pin", secret, make([]byte, 33)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if cfg, err := ClientTLSConfig(tt.secret, tt.pin); err == nil {
				t.Fatalf("ClientTLSConfig accepted its input and returned %v", cfg)
			}
		})
	}
}

func TestServerTLSConfig(t *testing.T) {
	b, _ := newTestBundle(t)
	cfg, err := b.ServerTLSConfig()
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}

	broken := *b
	broken.Secret = "short"
	if _, err := broken.ServerTLSConfig(); err == nil {
		t.Fatal("ServerTLSConfig accepted an invalid bundle")
	}
}
