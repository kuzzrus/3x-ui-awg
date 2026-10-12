package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
)

// fakeRunner is the listener of the decoy, which a test steers and which keeps what it was asked to do.
type fakeRunner struct {
	mu       sync.Mutex
	running  bool
	startErr error
	cert     frontproxy.CertStatus
	started  []frontproxy.Options
	reloaded []frontproxy.Options
	stops    int
}

func (r *fakeRunner) Start(opts frontproxy.Options) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, opts)
	if r.startErr != nil {
		return r.startErr
	}
	r.running = true
	return nil
}

func (r *fakeRunner) Reload(opts frontproxy.Options) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reloaded = append(r.reloaded, opts)
}

func (r *fakeRunner) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stops++
	r.running = false
	return nil
}

func (r *fakeRunner) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}

func (r *fakeRunner) CertStatus() frontproxy.CertStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cert
}

func (r *fakeRunner) counts() (started, reloaded, stops int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started), len(r.reloaded), r.stops
}

func (r *fakeRunner) lastStart() frontproxy.Options {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started[len(r.started)-1]
}

type decoyFixture struct {
	t      *testing.T
	state  *State
	runner *fakeRunner
	host   *decoyHost
	clock  time.Time
}

func newDecoyFixture(t *testing.T) *decoyFixture {
	t.Helper()
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &decoyFixture{t: t, state: state, runner: &fakeRunner{}, clock: time.Unix(1_700_000_000, 0)}
	f.host = f.hostOn(state)
	return f
}

func (f *decoyFixture) hostOn(state *State) *decoyHost {
	host := newDecoyHost(state, f.runner, "node-seed", "/state/decoy-certs")
	host.now = func() time.Time { return f.clock }
	return host
}

func (f *decoyFixture) set(spec agentproto.DecoySpec) agentproto.Decoy {
	f.t.Helper()
	view, err := f.host.Set(spec)
	if err != nil {
		f.t.Fatalf("Set(%+v): %v", spec, err)
	}
	return view
}

func page(template string) agentproto.DecoySpec {
	return agentproto.DecoySpec{Mode: agentproto.DecoyTemplate, Template: template, Domain: "node.example", Email: "ops@node.example"}
}

func TestDecoyHostServesOnlyTheDecoyOnTheLoopback(t *testing.T) {
	f := newDecoyFixture(t)

	view := f.set(page("parked"))
	if view.State.State != agentproto.DecoyStateStarting || view.Revision == "" || view.Spec.Port != agentproto.DefaultDecoyPort {
		t.Fatalf("answer = %+v, want the decoy as normalized, not started yet", view)
	}

	f.host.reconcile()
	started, _, _ := f.runner.counts()
	opts := f.runner.lastStart()
	if started != 1 || !opts.DecoyOnly || opts.Listen != "127.0.0.1" || opts.Port != agentproto.DefaultDecoyPort {
		t.Fatalf("started %d times with %+v, want one start of a decoy-only listener on the loopback at the default port", started, opts)
	}
	if opts.Decoy.Mode != frontproxy.DecoyTemplate || opts.Decoy.Template != "parked" || opts.Decoy.Seed != "node-seed" {
		t.Fatalf("decoy options = %+v, want the page chosen, rendered with the seed of the node", opts.Decoy)
	}
	if opts.TLS.Mode != frontproxy.CertAuto || opts.TLS.Domain != "node.example" || opts.TLS.Email != "ops@node.example" || opts.TLS.StorageDir != "/state/decoy-certs" {
		t.Fatalf("TLS options = %+v, want an automatic certificate for the domain, kept in the state folder", opts.TLS)
	}
	if got := f.host.State().State; got != agentproto.DecoyStateRunning {
		t.Fatalf("state = %q after the start, want running", got)
	}
	f.host.reconcile()
	if started, reloaded, _ := f.runner.counts(); started != 1 || reloaded != 0 {
		t.Fatalf("a pass over a running decoy started %d and reloaded %d times", started, reloaded)
	}
}

func TestDecoyHostTakesFilesOnTheNodeAndAProxy(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(agentproto.DecoySpec{
		Mode: agentproto.DecoyProxy, ProxyURL: "https://site.example/", Domain: "node.example",
		Cert: agentproto.DecoyCertManual, CertFile: "/etc/ssl/n.pem", KeyFile: "/etc/ssl/n.key", Port: 7500,
	})
	f.host.reconcile()

	opts := f.runner.lastStart()
	if opts.Decoy.Mode != frontproxy.DecoyProxy || opts.Decoy.ProxyURL != "https://site.example/" || opts.Port != 7500 {
		t.Fatalf("options = %+v, want the proxy decoy on the port asked for", opts)
	}
	if !opts.Decoy.PublicOnly {
		t.Fatal("the listener of an agent may dial a private address for whoever probes the node")
	}
	if opts.TLS.Mode != frontproxy.CertManual || opts.TLS.CertFile != "/etc/ssl/n.pem" || opts.TLS.KeyFile != "/etc/ssl/n.key" {
		t.Fatalf("TLS options = %+v, want the files of the node", opts.TLS)
	}
}

func TestDecoyHostChangesThePageWithoutRestartingTheListener(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))
	f.host.reconcile()

	f.set(page("tetris"))
	f.host.reconcile()
	started, reloaded, stops := f.runner.counts()
	if started != 1 || reloaded != 1 || stops != 0 {
		t.Fatalf("started %d, reloaded %d, stopped %d; want the page swapped in the running listener", started, reloaded, stops)
	}
}

func TestDecoyHostRestartsTheListenerWhenItsSettingsChange(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))
	f.host.reconcile()

	other := page("parked")
	other.Port = 7600
	f.set(other)
	f.host.reconcile()
	started, reloaded, stops := f.runner.counts()
	if started != 2 || reloaded != 0 || stops != 1 || f.runner.lastStart().Port != 7600 {
		t.Fatalf("started %d, reloaded %d, stopped %d, port %d; want a restart onto the new port", started, reloaded, stops, f.runner.lastStart().Port)
	}

	other.Domain = "other.example"
	f.set(other)
	f.host.reconcile()
	if started, _, _ := f.runner.counts(); started != 3 || f.runner.lastStart().TLS.Domain != "other.example" {
		t.Fatalf("started %d times, want another restart for another certificate", started)
	}
}

func TestDecoyHostStopsWhenTheMasterTakesTheDecoyOff(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))
	f.host.reconcile()

	view := f.set(agentproto.DecoySpec{})
	if view.Revision != "" || view.Spec != (agentproto.DecoySpec{}) || view.State.State != agentproto.DecoyStateOff {
		t.Fatalf("answer to no decoy = %+v, want an empty revision and the state off", view)
	}
	f.host.reconcile()
	if _, _, stops := f.runner.counts(); stops != 1 || f.runner.IsRunning() {
		t.Fatalf("stops = %d, running = %v; want the listener stopped once", stops, f.runner.IsRunning())
	}
	f.host.reconcile()
	if _, _, stops := f.runner.counts(); stops != 1 {
		t.Fatalf("a pass over a stopped decoy stopped it again: %d stops", stops)
	}
}

func TestDecoyHostLeavesADecoyThatWillNotStartAloneForAWhile(t *testing.T) {
	f := newDecoyFixture(t)
	f.runner.startErr = errors.New("listen tcp 127.0.0.1:7443: bind: address already in use")
	f.set(page("parked"))
	f.host.reconcile()

	state := f.host.State()
	if state.State != agentproto.DecoyStateFailed || !strings.Contains(state.Message, "address already in use") {
		t.Fatalf("state = %+v, want failed with why", state)
	}
	f.host.reconcile()
	f.clock = f.clock.Add(decoyRestartAfter - time.Second)
	f.host.reconcile()
	if started, _, _ := f.runner.counts(); started != 1 {
		t.Fatalf("started %d times inside the wait, want the one attempt", started)
	}

	f.runner.mu.Lock()
	f.runner.startErr = nil
	f.runner.mu.Unlock()
	f.clock = f.clock.Add(2 * time.Second)
	f.host.reconcile()
	if started, _, _ := f.runner.counts(); started != 2 || f.host.State().State != agentproto.DecoyStateRunning {
		t.Fatalf("started %d times, state %+v; want it running after the wait", started, f.host.State())
	}
}

func TestDecoyHostTriesAnotherDecoyAtOnceAfterAFailure(t *testing.T) {
	f := newDecoyFixture(t)
	f.runner.startErr = errors.New("no way")
	f.set(page("parked"))
	f.host.reconcile()

	f.runner.mu.Lock()
	f.runner.startErr = nil
	f.runner.mu.Unlock()
	f.set(page("tetris"))
	f.host.reconcile()
	if started, _, _ := f.runner.counts(); started != 2 || f.host.State().State != agentproto.DecoyStateRunning {
		t.Fatalf("started %d times, state %+v; want the new decoy served without waiting out the failure", started, f.host.State())
	}
}

func TestDecoyHostSaysHowTheCertificateStands(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))
	f.host.reconcile()
	notAfter := time.Unix(1_800_000_000, 0)

	for name, tc := range map[string]struct {
		cert frontproxy.CertStatus
		want agentproto.DecoyState
	}{
		"not heard of yet": {frontproxy.CertStatus{}, agentproto.DecoyState{State: agentproto.DecoyStateRunning}},
		"being obtained": {
			frontproxy.CertStatus{State: frontproxy.CertStateObtaining},
			agentproto.DecoyState{State: agentproto.DecoyStateRunning, Cert: agentproto.DecoyCertObtaining},
		},
		"obtained": {
			frontproxy.CertStatus{State: frontproxy.CertStateObtained, NotAfter: &notAfter},
			agentproto.DecoyState{State: agentproto.DecoyStateRunning, Cert: agentproto.DecoyCertObtained, CertNotAfter: notAfter.Unix()},
		},
		"refused by the CA": {
			frontproxy.CertStatus{State: frontproxy.CertStateFailed, Error: "acme: error: 400 :: urn:ietf:params:acme:error:dns :: no valid A records"},
			agentproto.DecoyState{State: agentproto.DecoyStateRunning, Cert: agentproto.DecoyCertFailed, Message: "acme: error: 400 :: urn:ietf:params:acme:error:dns :: no valid A records"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f.runner.mu.Lock()
			f.runner.cert = tc.cert
			f.runner.mu.Unlock()
			if got := f.host.State(); got != tc.want {
				t.Fatalf("state = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecoyHostKeepsTheDecoyAcrossARestart(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))

	again := f.hostOn(f.state)
	if again.Revision() != f.host.Revision() || again.Revision() == "" {
		t.Fatalf("revision after a restart = %q, want the one of the stored decoy %q", again.Revision(), f.host.Revision())
	}
	again.reconcile()
	if started, _, _ := f.runner.counts(); started != 1 {
		t.Fatalf("started %d times, want the stored decoy served with the master out of reach", started)
	}
}

func TestDecoyHostRefusesWhatItCannotServe(t *testing.T) {
	f := newDecoyFixture(t)
	f.set(page("parked"))

	_, err := f.host.Set(page("no-such-page"))
	var refused *agentproto.DecoyError
	if !errors.As(err, &refused) || !strings.Contains(refused.Reason, "no-such-page") {
		t.Fatalf("Set error = %v, want the decoy refused with the page named", err)
	}
	if stored, err := f.state.Decoy(); err != nil || stored.Template != "parked" {
		t.Fatalf("stored decoy = %+v, %v; want the one from before the refusal", stored, err)
	}
	if f.host.View().Spec.Template != "parked" {
		t.Fatalf("decoy after a refusal = %+v, want it unchanged", f.host.View().Spec)
	}
}

func TestStateServesNoDecoyItCannotRead(t *testing.T) {
	dir := t.TempDir()
	state, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"a page this build does not have": `{"mode":"template","template":"gone","domain":"a.example"}`,
		"not json":                        `{"mode":`,
	} {
		if err := os.WriteFile(filepath.Join(dir, decoyFile), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := state.Decoy(); err == nil {
			t.Errorf("%s: the stored decoy was taken for one", name)
		}
		if host := newDecoyHost(state, &fakeRunner{}, "seed", dir); host.Revision() != "" {
			t.Errorf("%s: a host started on it serves revision %q, want none until the master says", name, host.Revision())
		}
	}
}

func TestDecoyHostFollowsTheMasterAndStopsWhenTheContextEnds(t *testing.T) {
	f := newDecoyFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.host.Run(ctx)
	}()
	f.set(page("parked"))
	waitForHost(t, "the decoy to be served", f.runner.IsRunning)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after its context ended")
	}
	if f.runner.IsRunning() {
		t.Fatal("the decoy was left served when the agent stopped")
	}
}

// The listener answers anyone who probes the node, so a site that is not on a public address is not one it may proxy.
func TestDecoyHostRefusesAProxyToAPrivateAddress(t *testing.T) {
	f := newDecoyFixture(t)
	kept := f.set(page("parked")).Revision
	for _, site := range []string{
		"http://127.0.0.1:2053", "http://localhost/", "http://10.0.0.5/", "http://192.168.1.1/",
		"http://169.254.169.254/latest/meta-data/", "http://[::1]/", "http://[fd00::1]/",
	} {
		_, err := f.host.Set(agentproto.DecoySpec{Mode: agentproto.DecoyProxy, ProxyURL: site, Domain: "node.example"})
		var refused *agentproto.DecoyError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "public address") {
			t.Errorf("a proxy to %s: %v, want a refusal that says the site is not on a public address", site, err)
		}
	}
	if got := f.host.View().Revision; got != kept {
		t.Fatalf("revision = %q after the refusals, want %q from before", got, kept)
	}
}
