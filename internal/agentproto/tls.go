package agentproto

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"strings"
)

// ServerTLSConfig is the agent side: TLS 1.3 only, and the handshake is
// aborted before any certificate is sent unless the client names the SNI.
func (b *Bundle) ServerTLSConfig() (*tls.Config, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	cert, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM))
	if err != nil {
		return nil, err
	}
	want, err := b.SNI()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{cert},
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			if !equalConstantTime(strings.ToLower(hello.ServerName), want) {
				return nil, errors.New("agentproto: unexpected server name")
			}
			return nil, nil
		},
	}, nil
}

// ClientTLSConfig is the master side: it dials with the derived SNI and trusts
// exactly the pinned certificate, with no chain or name verification.
func ClientTLSConfig(secret string, pin []byte) (*tls.Config, error) {
	sni, err := SNIFor(secret)
	if err != nil {
		return nil, err
	}
	if len(pin) != sha256.Size {
		return nil, errors.New("agent certificate pin must be a SHA-256 hash")
	}
	return &tls.Config{
		ServerName:         sni,
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // lgtm[go/disabled-certificate-check]
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("agent presented no certificate")
			}
			sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(sum[:], pin) != 1 {
				return errors.New("agent certificate does not match the pinned SHA-256")
			}
			return nil
		},
	}, nil
}
