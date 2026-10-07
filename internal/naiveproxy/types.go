package naiveproxy

// Client is one Naive user's HTTP Basic-Auth credential, rendered into the
// Caddyfile as its own basic_auth line.
type Client struct {
	Email    string
	Username string
	Password string
}

// Instance is everything one Naive-backed inbound needs to run its own Caddy
// process. Package-local, mirrors internal/mtproto's Instance.
type Instance struct {
	Id         int
	Tag        string // the inbound's own tag, only used to roll metered traffic up to it
	ListenAddr string // loopback "127.0.0.1:PORT" this Caddy binds to
	Domain     string // the SNI frontproxy's SNI-relay matches to reach ListenAddr

	// The certificate Caddy serves for Domain. The admin supplies the files in CertManual;
	// in CertAuto the Manager fills them in once the certificate has been issued.
	CertMode string
	CertFile string
	KeyFile  string
	// CertDigest changes whenever the pair does, so a renewal restarts Caddy (see renderCaddyfile).
	CertDigest string

	Clients []Client

	// Mirror MTProto's identically named fields: when set, Caddy dials
	// outbound traffic through this loopback SOCKS5 port (an Xray inbound).
	RouteThroughXray bool
	XrayRoutePort    int
}
