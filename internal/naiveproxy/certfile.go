package naiveproxy

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

var domainLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func normalizeDomain(domain string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
}

// ValidCertDomain says whether Let's Encrypt can issue for domain over HTTP-01: a plain DNS
// name, no wildcard (that needs DNS-01) and no IP address.
func ValidCertDomain(domain string) error {
	d := normalizeDomain(domain)
	switch {
	case d == "":
		return errors.New("a domain is required")
	case strings.Contains(d, "*"):
		return errors.New("a wildcard certificate cannot be ordered over HTTP-01")
	case net.ParseIP(d) != nil:
		return errors.New("an IP address cannot get a Let's Encrypt certificate, use a domain name")
	case len(d) > 253:
		return errors.New("the domain is too long")
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return fmt.Errorf("%q is not a fully qualified domain name", domain)
	}
	for _, label := range labels {
		if !domainLabel.MatchString(label) {
			return fmt.Errorf("%q is not a valid domain name (internationalised names need their xn-- form)", domain)
		}
	}
	return nil
}

// certLeaf parses the first certificate in the PEM file at path.
func certLeaf(path string) (*x509.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("no certificate found in %s", path)
		}
		if block.Type == "CERTIFICATE" {
			return x509.ParseCertificate(block.Bytes)
		}
	}
}

// certDigest fingerprints a certificate and key file pair, "" when either is unreadable. Fed into
// the Caddyfile, it makes a replaced pair restart Caddy, which only reads the files at start.
func certDigest(certFile, keyFile string) string {
	h := sha256.New()
	for _, path := range []string{certFile, keyFile} {
		if path == "" {
			return ""
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return ""
		}
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ManualCertStatus reports the expiry of a certificate file the admin maintains.
func ManualCertStatus(certFile string) CertStatus {
	st := CertStatus{Mode: CertManual}
	if certFile == "" {
		st.State, st.Error = CertStateFailed, "no certificate file is set"
		return st
	}
	leaf, err := certLeaf(certFile)
	if err != nil {
		st.State, st.Error = CertStateFailed, err.Error()
		return st
	}
	notAfter := leaf.NotAfter
	st.State, st.NotAfter = CertStateObtained, &notAfter
	return st
}
