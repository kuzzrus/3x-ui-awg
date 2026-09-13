package tproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

// bridgeContext is PROTOCOL.md's frozen v1 domain-separation label for a
// root-path deployment (no base_path) -- this fork never sets one.
const bridgeContext = "tdesktop-web-proxy-bridge-v1\n"

// DeriveCapability computes the bridge capability a request's ?bridge= query
// must match, per PROTOCOL.md's own published formula and test vectors.
func DeriveCapability(hostname, secret string) (string, error) {
	normalized, err := normalizeSecret(secret)
	if err != nil {
		return "", err
	}
	key, err := hex.DecodeString(normalized)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(bridgeContext + strings.ToLower(strings.TrimSpace(hostname))))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
