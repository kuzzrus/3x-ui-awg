package agentproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/mhsanaei/3x-ui/v3/internal/util/netsafe"
)

// What an agent serves to whoever probes the port the REALITY fallback points at: nothing, one of the pages
// built into the agent, a reverse proxy to a site elsewhere, or an AdGuard Home that the agent runs for it.
const (
	DecoyNone     = ""
	DecoyTemplate = "template"
	DecoyProxy    = "proxy"
	DecoyAdGuard  = "adguard"
)

// Where the certificate of the decoy comes from: ACME over HTTP-01 on port 80, or files on the node.
const (
	DecoyCertAuto   = "auto"
	DecoyCertManual = "manual"
)

const (
	// DefaultDecoyTemplate is the page of a decoy that names none.
	DefaultDecoyTemplate = "maintenance"
	// DefaultDecoyPort is the loopback port of a decoy that names none.
	DefaultDecoyPort = 7443

	minDecoyPort    = 1024
	maxDecoyField   = 512
	maxDecoyDomain  = 253
	maxDecoyAddress = 254
)

// DecoySpec is the body of PathDecoy. The zero value is no decoy, which stops a running one.
type DecoySpec struct {
	Mode     string `json:"mode"`
	Template string `json:"template,omitempty"`
	ProxyURL string `json:"proxyUrl,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Cert     string `json:"cert,omitempty"`
	Email    string `json:"email,omitempty"`
	CertFile string `json:"certFile,omitempty"`
	KeyFile  string `json:"keyFile,omitempty"`
	Port     int    `json:"port,omitempty"`
}

// How the decoy stands on the agent.
const (
	DecoyStateOff      = "off"
	DecoyStateStarting = "starting"
	DecoyStateRunning  = "running"
	DecoyStateFailed   = "failed"
)

// Where its certificate stands, which is the question once the listener is up: an ACME round may be
// under way or may have failed for a name that does not point at the node.
const (
	DecoyCertObtaining = "obtaining"
	DecoyCertObtained  = "obtained"
	DecoyCertFailed    = "failed"
)

// DecoyState is how the decoy stands. Message is why it failed to start or why its certificate failed.
type DecoyState struct {
	State        string `json:"state"`
	Cert         string `json:"cert,omitempty"`
	CertNotAfter int64  `json:"certNotAfter,omitempty"` // unix seconds
	Message      string `json:"message,omitempty"`
}

// Decoy is the answer of PathDecoy: the decoy as the agent keeps it, and how it stands.
type Decoy struct {
	Revision string     `json:"revision"`
	Spec     DecoySpec  `json:"spec"`
	State    DecoyState `json:"state"`
}

// DecoyError is a decoy that cannot be served, as opposed to one that could not be kept.
type DecoyError struct{ Reason string }

func (e *DecoyError) Error() string { return e.Reason }

func decoyError(format string, args ...any) error {
	return &DecoyError{Reason: fmt.Sprintf(format, args...)}
}

// DecoyRules is what a decoy may be made of that only the side with the pages built in knows.
type DecoyRules struct {
	Templates []string
	// PrivateProxy lets a proxied site be on a private address, which only a test of the proxy wants: whoever
	// probes the node can reach whatever the decoy does.
	PrivateProxy bool
}

var domainPattern = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// NormalizeDecoy checks a decoy and returns it in the form both ends compare and store: the defaults are
// filled in, what its mode does not use is cleared, and no decoy at all is the zero value.
func NormalizeDecoy(spec DecoySpec, rules DecoyRules) (DecoySpec, error) {
	out := DecoySpec{Mode: strings.TrimSpace(spec.Mode)}
	switch out.Mode {
	case DecoyNone:
		return DecoySpec{}, nil
	case DecoyTemplate:
		out.Template = strings.TrimSpace(spec.Template)
		if out.Template == "" {
			out.Template = DefaultDecoyTemplate
		}
		if !slices.Contains(rules.Templates, out.Template) {
			return DecoySpec{}, decoyError("unknown decoy page %q", out.Template)
		}
	case DecoyProxy:
		out.ProxyURL = strings.TrimSpace(spec.ProxyURL)
		if err := checkProxyURL(out.ProxyURL, rules.PrivateProxy); err != nil {
			return DecoySpec{}, err
		}
	case DecoyAdGuard:
	default:
		return DecoySpec{}, decoyError("unknown decoy mode %q", out.Mode)
	}

	out.Domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(spec.Domain)), ".")
	if len(out.Domain) > maxDecoyDomain || !domainPattern.MatchString(out.Domain) {
		return DecoySpec{}, decoyError("the decoy needs the domain name its certificate is for, not %q", spec.Domain)
	}

	switch strings.TrimSpace(spec.Cert) {
	case "", DecoyCertAuto:
		out.Cert = DecoyCertAuto
		out.Email = strings.TrimSpace(spec.Email)
		if err := checkEmail(out.Email); err != nil {
			return DecoySpec{}, err
		}
	case DecoyCertManual:
		out.Cert = DecoyCertManual
		out.CertFile, out.KeyFile = strings.TrimSpace(spec.CertFile), strings.TrimSpace(spec.KeyFile)
		for _, path := range []string{out.CertFile, out.KeyFile} {
			if err := checkNodePath(path); err != nil {
				return DecoySpec{}, err
			}
		}
	default:
		return DecoySpec{}, decoyError("unknown decoy certificate source %q", spec.Cert)
	}

	out.Port = spec.Port
	if out.Port == 0 {
		out.Port = DefaultDecoyPort
	}
	if out.Port < minDecoyPort || out.Port > 65535 {
		return DecoySpec{}, decoyError("the decoy port must be %d-65535, not %d", minDecoyPort, spec.Port)
	}
	return out, nil
}

// checkProxyURL refuses what the panel refuses of its own decoy: a site that is not on a public address, since
// the probes that the decoy answers come from anywhere and would reach the node's own network through it.
func checkProxyURL(raw string, private bool) error {
	target, err := url.Parse(raw)
	switch {
	case raw == "" || len(raw) > maxDecoyField:
		return decoyError("the decoy needs the address of the site to proxy, at most %d characters", maxDecoyField)
	case err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "":
		return decoyError("the decoy site must be an http or https address, not %q", raw)
	case target.User != nil:
		return decoyError("the address of the decoy site must not carry a password")
	case !private && privateHost(target.Hostname()):
		return decoyError("the decoy site must be on a public address, and %q is not", target.Hostname())
	}
	return nil
}

// privateHost is a name that can only mean this machine, or an address literal on a private, loopback or
// link-local network. A name that resolves to one is the dialler's to refuse, where it is looked up.
func privateHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && netsafe.IsBlockedIP(ip)
}

func checkEmail(email string) error {
	if email == "" {
		return nil
	}
	name, host, found := strings.Cut(email, "@")
	if !found || name == "" || host == "" || strings.Contains(host, "@") || len(email) > maxDecoyAddress ||
		strings.IndexFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return decoyError("%q is not an email address", email)
	}
	return nil
}

// checkNodePath wants an absolute path on the node, which is a Linux machine, and nothing that a
// file name cannot hold.
func checkNodePath(path string) error {
	if !strings.HasPrefix(path, "/") || len(path) > maxDecoyField ||
		strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return decoyError("a certificate file of the decoy must be an absolute path on the node, not %q", path)
	}
	return nil
}

// DecoyRevision identifies a normalized decoy, and is empty for none, which is what an agent that has
// never been given one reports.
func DecoyRevision(spec DecoySpec) string {
	if spec.Mode == DecoyNone {
		return ""
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
