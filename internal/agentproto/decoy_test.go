package agentproto

import (
	"strings"
	"testing"
)

var testRules = DecoyRules{Templates: []string{"maintenance", "parked", "tetris"}}

func TestNormalizeDecoyFillsTheDefaultsAndClearsWhatTheModeDoesNotUse(t *testing.T) {
	for name, tc := range map[string]struct {
		in   DecoySpec
		want DecoySpec
	}{
		"none clears everything": {
			in:   DecoySpec{Template: "parked", Domain: "a.example", Port: 9000},
			want: DecoySpec{},
		},
		"a page with the defaults": {
			in:   DecoySpec{Mode: DecoyTemplate, Domain: " A.Example. "},
			want: DecoySpec{Mode: DecoyTemplate, Template: DefaultDecoyTemplate, Domain: "a.example", Cert: DecoyCertAuto, Port: DefaultDecoyPort},
		},
		"a page keeps no proxy address and no files": {
			in: DecoySpec{
				Mode: DecoyTemplate, Template: "parked", ProxyURL: "https://x.example", Domain: "a.example",
				Cert: DecoyCertAuto, Email: "ops@a.example", CertFile: "/c", KeyFile: "/k", Port: 7500,
			},
			want: DecoySpec{Mode: DecoyTemplate, Template: "parked", Domain: "a.example", Cert: DecoyCertAuto, Email: "ops@a.example", Port: 7500},
		},
		"a proxy keeps no page": {
			in:   DecoySpec{Mode: DecoyProxy, Template: "parked", ProxyURL: " https://site.example/home ", Domain: "a.example"},
			want: DecoySpec{Mode: DecoyProxy, ProxyURL: "https://site.example/home", Domain: "a.example", Cert: DecoyCertAuto, Port: DefaultDecoyPort},
		},
		"files on the node keep no email": {
			in:   DecoySpec{Mode: DecoyTemplate, Domain: "a.example", Cert: DecoyCertManual, Email: "ops@a.example", CertFile: "/etc/ssl/a.pem", KeyFile: "/etc/ssl/a.key"},
			want: DecoySpec{Mode: DecoyTemplate, Template: DefaultDecoyTemplate, Domain: "a.example", Cert: DecoyCertManual, CertFile: "/etc/ssl/a.pem", KeyFile: "/etc/ssl/a.key", Port: DefaultDecoyPort},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeDecoy(tc.in, testRules)
			if err != nil || got != tc.want {
				t.Fatalf("NormalizeDecoy = %+v, %v, want %+v", got, err, tc.want)
			}
			if again, err := NormalizeDecoy(got, testRules); err != nil || again != got {
				t.Fatalf("a normalized decoy is changed by normalizing it again: %+v, %v", again, err)
			}
		})
	}
}

func TestNormalizeDecoyRefusesWhatTheAgentCannotServe(t *testing.T) {
	page := DecoySpec{Mode: DecoyTemplate, Domain: "a.example"}
	with := func(edit func(*DecoySpec)) DecoySpec {
		spec := page
		edit(&spec)
		return spec
	}
	for name, tc := range map[string]struct {
		in   DecoySpec
		want string
	}{
		"a mode nobody serves":     {with(func(s *DecoySpec) { s.Mode = "upload" }), `unknown decoy mode "upload"`},
		"a page that is not there": {with(func(s *DecoySpec) { s.Template = "../etc/passwd" }), "unknown decoy page"},
		"no domain":                {with(func(s *DecoySpec) { s.Domain = "" }), "domain name"},
		"an address, not a name":   {with(func(s *DecoySpec) { s.Domain = "203.0.113.7" }), "domain name"},
		"a name with a port":       {with(func(s *DecoySpec) { s.Domain = "a.example:443" }), "domain name"},
		"a name with a path":       {with(func(s *DecoySpec) { s.Domain = "a.example/x" }), "domain name"},
		"a single label":           {with(func(s *DecoySpec) { s.Domain = "localhost" }), "domain name"},
		"a proxy with no address":  {DecoySpec{Mode: DecoyProxy, Domain: "a.example"}, "address of the site"},
		"a proxy to a file":        {DecoySpec{Mode: DecoyProxy, Domain: "a.example", ProxyURL: "file:///etc/passwd"}, "http or https"},
		"a proxy with a password":  {DecoySpec{Mode: DecoyProxy, Domain: "a.example", ProxyURL: "https://u:p@site.example"}, "password"},
		"a proxy with no host":     {DecoySpec{Mode: DecoyProxy, Domain: "a.example", ProxyURL: "https://"}, "http or https"},
		"a certificate source":     {with(func(s *DecoySpec) { s.Cert = "self" }), "certificate source"},
		"an email with no name":    {with(func(s *DecoySpec) { s.Email = "@a.example" }), "not an email"},
		"an email with a space":    {with(func(s *DecoySpec) { s.Email = "a b@a.example" }), "not an email"},
		"no certificate file":      {with(func(s *DecoySpec) { s.Cert, s.KeyFile = DecoyCertManual, "/k" }), "absolute path"},
		"a relative key file":      {with(func(s *DecoySpec) { s.Cert, s.CertFile, s.KeyFile = DecoyCertManual, "/c", "k.pem" }), "absolute path"},
		"a path with a newline":    {with(func(s *DecoySpec) { s.Cert, s.CertFile, s.KeyFile = DecoyCertManual, "/c\nx", "/k" }), "absolute path"},
		"a port below the range":   {with(func(s *DecoySpec) { s.Port = 80 }), "1024-65535"},
		"a port past the range":    {with(func(s *DecoySpec) { s.Port = 70000 }), "1024-65535"},
		"a negative port":          {with(func(s *DecoySpec) { s.Port = -1 }), "1024-65535"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := NormalizeDecoy(tc.in, testRules)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("NormalizeDecoy(%+v) = %+v, %v, want an error that says %q", tc.in, got, err, tc.want)
			}
		})
	}
}

// Whoever probes the node reaches the site it proxies, so that site is on a public address, as the panel's own decoy
// proxy has to be. A name that resolves to a private one is left to the dialler of the agent, which looks it up.
func TestNormalizeDecoyRefusesAProxyToAPrivateHost(t *testing.T) {
	for _, site := range []string{
		"http://127.0.0.1:2053", "http://127.1.2.3/", "http://localhost/", "http://LOCALHOST./", "https://panel.localhost/",
		"http://10.0.0.5/", "http://172.16.0.1/", "http://192.168.1.1:8080/", "http://169.254.169.254/latest/meta-data/",
		"http://100.64.0.1/", "http://0.0.0.0/", "http://[::1]/", "http://[::ffff:127.0.0.1]/", "http://[fd00::1]/", "http://[fe80::1]/",
	} {
		spec := DecoySpec{Mode: DecoyProxy, Domain: "a.example", ProxyURL: site}
		if got, err := NormalizeDecoy(spec, testRules); err == nil || !strings.Contains(err.Error(), "public address") {
			t.Errorf("a proxy to %s was taken: %+v, %v", site, got, err)
		}
		private := testRules
		private.PrivateProxy = true
		if _, err := NormalizeDecoy(spec, private); err != nil {
			t.Errorf("a proxy to %s was refused although private sites were allowed: %v", site, err)
		}
	}
	for _, site := range []string{"https://site.example/", "http://8.8.8.8/", "https://[2606:4700:4700::1111]/"} {
		spec := DecoySpec{Mode: DecoyProxy, Domain: "a.example", ProxyURL: site}
		if _, err := NormalizeDecoy(spec, testRules); err != nil {
			t.Errorf("a proxy to %s was refused: %v", site, err)
		}
	}
}

func TestDecoyRevision(t *testing.T) {
	if got := DecoyRevision(DecoySpec{}); got != "" {
		t.Fatalf("no decoy has revision %q, want none, which is what an agent that predates it reports", got)
	}
	base, err := NormalizeDecoy(DecoySpec{Mode: DecoyTemplate, Domain: "a.example"}, testRules)
	if err != nil {
		t.Fatal(err)
	}
	want := DecoyRevision(base)
	if want == "" || want != DecoyRevision(base) {
		t.Fatalf("the revision of a decoy is %q, want a value that is the same each time", want)
	}
	for name, edit := range map[string]func(*DecoySpec){
		"the page":   func(s *DecoySpec) { s.Template = "parked" },
		"the domain": func(s *DecoySpec) { s.Domain = "b.example" },
		"the port":   func(s *DecoySpec) { s.Port = 9000 },
		"the email":  func(s *DecoySpec) { s.Email = "ops@a.example" },
	} {
		changed := base
		edit(&changed)
		if DecoyRevision(changed) == want {
			t.Errorf("a decoy with another %s has the same revision", name)
		}
	}
}
