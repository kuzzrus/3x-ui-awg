package agentproto

import (
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

const (
	// BundlePrefix names the format and version of an encoded pairing bundle.
	BundlePrefix   = "xab1."
	bundleVersion  = 1
	maxBundleBytes = 16 << 10
	maxHostLen     = 253
	maxLabelLen    = 63
)

// Bundle is everything an agent needs to serve one master. It carries the
// private key, so it is a credential and is never stored by the master.
type Bundle struct {
	Version int    `json:"v"`
	Address string `json:"address"`
	Port    int    `json:"port"`
	Secret  string `json:"secret"`
	CertPEM string `json:"cert"`
	KeyPEM  string `json:"key"`
}

// NewBundle mints the secret and the pinned identity for a new agent. The
// returned fingerprint is what the master stores to pin the agent.
func NewBundle(address string, port int, now time.Time) (*Bundle, string, error) {
	if err := validateEndpoint(address, port); err != nil {
		return nil, "", err
	}
	secret, err := NewSecret()
	if err != nil {
		return nil, "", err
	}
	sni, err := SNIFor(secret)
	if err != nil {
		return nil, "", err
	}
	id, err := GenerateIdentity(address, sni, now)
	if err != nil {
		return nil, "", err
	}
	return &Bundle{
		Version: bundleVersion,
		Address: address,
		Port:    port,
		Secret:  secret,
		CertPEM: id.CertPEM,
		KeyPEM:  id.KeyPEM,
	}, id.Fingerprint, nil
}

// Encode renders the bundle as a single copy-pasteable token.
func (b *Bundle) Encode() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	return BundlePrefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// ParseBundle decodes and validates a token produced by Encode.
func ParseBundle(token string) (*Bundle, error) {
	token = strings.TrimSpace(token)
	body, ok := strings.CutPrefix(token, BundlePrefix)
	if !ok {
		return nil, errors.New("pairing bundle has an unknown format")
	}
	if len(body) > base64.RawURLEncoding.EncodedLen(maxBundleBytes) {
		return nil, errors.New("pairing bundle is too large")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("pairing bundle is not base64url: %w", err)
	}
	var b Bundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("pairing bundle is not valid: %w", err)
	}
	if err := b.Validate(); err != nil {
		return nil, err
	}
	return &b, nil
}

// Validate checks the endpoint, the secret and that the key matches the cert.
func (b *Bundle) Validate() error {
	if b.Version != bundleVersion {
		return fmt.Errorf("pairing bundle version %d is not supported", b.Version)
	}
	if err := validateEndpoint(b.Address, b.Port); err != nil {
		return err
	}
	if _, err := DecodeSecret(b.Secret); err != nil {
		return err
	}
	if _, err := certDER(b.CertPEM); err != nil {
		return err
	}
	if _, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM)); err != nil {
		return fmt.Errorf("pairing bundle key does not match its certificate: %w", err)
	}
	return nil
}

// SNI is the server name the master must dial this agent with.
func (b *Bundle) SNI() (string, error) { return SNIFor(b.Secret) }

// Fingerprint is the pin of the bundle's certificate.
func (b *Bundle) Fingerprint() (string, error) {
	der, err := certDER(b.CertPEM)
	if err != nil {
		return "", err
	}
	return Fingerprint(der), nil
}

func validateEndpoint(address string, port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("agent port %d is out of range", port)
	}
	return validateAddress(address)
}

func validateAddress(address string) error {
	if ip, err := netip.ParseAddr(address); err == nil {
		if ip.Zone() != "" {
			return errors.New("agent address must not carry an IPv6 zone")
		}
		return nil
	}
	if address == "" || len(address) > maxHostLen {
		return errors.New("agent address must be a host name or IP address")
	}
	for _, label := range strings.Split(address, ".") {
		if !validLabel(label) {
			return fmt.Errorf("agent address %q is not a valid host name", address)
		}
	}
	return nil
}

func validLabel(label string) bool {
	if label == "" || len(label) > maxLabelLen || label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for _, r := range label {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}
