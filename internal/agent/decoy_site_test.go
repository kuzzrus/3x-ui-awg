package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/frontproxy"
)

// siteZip is the zip of a site, the files in a fixed order so that the same files make the same zip.
func siteZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, name := range slices.Sorted(maps.Keys(files)) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func (f *decoyFixture) store(files map[string]string) string {
	f.t.Helper()
	raw := siteZip(f.t, files)
	digest := digestOf(raw)
	stored, err := f.host.StoreSite(digest, bytes.NewReader(raw))
	if err != nil || stored != (agentproto.DecoySite{Sha256: digest, Size: int64(len(raw))}) {
		f.t.Fatalf("StoreSite = %+v, %v", stored, err)
	}
	return digest
}

func siteDecoy(digest string) agentproto.DecoySpec {
	return agentproto.DecoySpec{Mode: agentproto.DecoyUpload, Site: digest, Domain: "node.example", Email: "ops@node.example"}
}

func TestStoreSiteUnpacksTheZipUnderItsDigest(t *testing.T) {
	f := newDecoyFixture(t)
	digest := f.store(map[string]string{"index.html": "<html>home</html>", "css/site.css": "body{}"})

	for name, want := range map[string]string{"index.html": "<html>home</html>", "css/site.css": "body{}"} {
		got, err := os.ReadFile(filepath.Join(f.host.sitePath(digest), filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("%s = %q, %v, want %q", name, got, err, want)
		}
	}
	if got := f.host.Sites(); !slices.Equal(got, []string{digest}) {
		t.Fatalf("Sites() = %v, want only %s", got, digest)
	}
	entries, err := os.ReadDir(f.host.sites)
	if err != nil || len(entries) != 1 {
		t.Fatalf("the sites folder holds %v, %v, want the one site and no zip or staging folder left over", entries, err)
	}
}

func TestStoreSiteRefusesWhatIsNotTheSiteItIsNamedAs(t *testing.T) {
	raw := siteZip(t, map[string]string{"index.html": "<html>home</html>"})
	noIndex := siteZip(t, map[string]string{"about.html": "<html>about</html>"})
	for name, tc := range map[string]struct {
		digest string
		body   []byte
		want   string
	}{
		"a body that is not its digest": {digestOf([]byte("another")), raw, "does not match its sha256"},
		"an empty body":                 {digestOf(nil), nil, "empty"},
		"a body that is no zip":         {digestOf([]byte("not a zip")), []byte("not a zip"), "not a readable zip"},
		"a zip with no index page":      {digestOf(noIndex), noIndex, "no index.html"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newDecoyFixture(t)
			_, err := f.host.StoreSite(tc.digest, bytes.NewReader(tc.body))
			var refused *agentproto.DecoyError
			if !errors.As(err, &refused) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("StoreSite = %v, want a refusal that says %q", err, tc.want)
			}
			if got := f.host.Sites(); len(got) != 0 {
				t.Fatalf("Sites() = %v after a refusal, want none", got)
			}
			if entries, _ := os.ReadDir(f.host.sites); len(entries) != 0 {
				t.Fatalf("the sites folder holds %v after a refusal, want it empty", entries)
			}
		})
	}
}

// The folder of a site that is served now must not be replaced under the listener by a send of the same zip.
func TestStoreSiteLeavesASiteItHoldsAlone(t *testing.T) {
	f := newDecoyFixture(t)
	files := map[string]string{"index.html": "<html>home</html>"}
	digest := f.store(files)
	marker := filepath.Join(f.host.sitePath(digest), "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.store(files)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the folder of the site was unpacked again: %v", err)
	}
}

func TestSetRefusesASiteTheAgentDoesNotHold(t *testing.T) {
	f := newDecoyFixture(t)
	digest := digestOf([]byte("never sent"))

	_, err := f.host.Set(siteDecoy(digest))
	var refused *agentproto.DecoyError
	if !errors.As(err, &refused) || !strings.Contains(err.Error(), "send it first") {
		t.Fatalf("Set = %v, want a refusal that asks for the site first", err)
	}
	if got := f.host.Revision(); got != "" {
		t.Fatalf("revision = %q after the refusal, want the decoy untouched", got)
	}

	held := f.store(map[string]string{"index.html": "<html>home</html>"})
	if view, err := f.host.Set(siteDecoy(held)); err != nil || view.Spec.Site != held {
		t.Fatalf("Set after the site arrived = %+v, %v", view, err)
	}
}

func TestUploadDecoyServesTheSiteAndSwapsAChangedOneWithoutARestart(t *testing.T) {
	f := newDecoyFixture(t)
	first := f.store(map[string]string{"index.html": "<html>one</html>"})
	second := f.store(map[string]string{"index.html": "<html>two</html>"})

	f.set(siteDecoy(first))
	f.host.reconcile()
	opts := f.runner.lastStart()
	if opts.Decoy.Mode != frontproxy.DecoyUpload || opts.Decoy.Dir != f.host.sitePath(first) {
		t.Fatalf("decoy options = %+v, want the folder of the first site served as an upload", opts.Decoy)
	}

	f.set(siteDecoy(second))
	f.host.reconcile()
	started, reloaded, stops := f.runner.counts()
	if started != 1 || reloaded != 1 || stops != 0 {
		t.Fatalf("a changed site started %d, reloaded %d and stopped %d times, want one reload of the running listener", started, reloaded, stops)
	}
	if got := f.runner.lastReload().Decoy.Dir; got != f.host.sitePath(second) {
		t.Fatalf("reloaded with %q, want the folder of the second site", got)
	}
	if got := f.host.State().State; got != agentproto.DecoyStateRunning {
		t.Fatalf("state = %q, want running", got)
	}
}

// After a restart the agent may hold a decoy whose site is gone, and the master sends the site again; until it does, the
// listener must not start on a folder that is not there.
func TestUploadDecoyWaitsForItsSite(t *testing.T) {
	f := newDecoyFixture(t)
	raw := siteZip(t, map[string]string{"index.html": "<html>home</html>"})
	digest := digestOf(raw)
	spec, err := agentproto.NormalizeDecoy(siteDecoy(digest), decoyRules())
	if err != nil {
		t.Fatal(err)
	}
	if err := f.state.SaveDecoy(spec); err != nil {
		t.Fatal(err)
	}
	host := f.hostOn(f.state)

	host.reconcile()
	if started, _, _ := f.runner.counts(); started != 0 {
		t.Fatalf("the listener started %d times on a site that is not there", started)
	}
	if state := host.State(); state.State != agentproto.DecoyStateStarting || !strings.Contains(state.Message, "has not arrived") {
		t.Fatalf("state = %+v, want starting, and a message that the site has not arrived", state)
	}

	if _, err := host.StoreSite(digest, bytes.NewReader(raw)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-host.wake:
	default:
		t.Fatal("storing the site did not wake the loop, so the decoy would wait for the next look")
	}
	host.reconcile()
	if started, _, _ := f.runner.counts(); started != 1 {
		t.Fatalf("the listener started %d times once the site was there, want 1", started)
	}
}

func TestPruneSitesKeepsTheSitesInUseAndRemovesTheRest(t *testing.T) {
	f := newDecoyFixture(t)
	served := f.store(map[string]string{"index.html": "<html>served</html>"})
	wanted := f.store(map[string]string{"index.html": "<html>wanted</html>"})
	old := f.store(map[string]string{"index.html": "<html>old</html>"})
	fresh := f.store(map[string]string{"index.html": "<html>fresh</html>"})

	f.set(siteDecoy(served))
	f.host.reconcile()
	f.set(siteDecoy(wanted)) // not applied yet: the listener still runs the first

	leftovers := []string{".site-1234.zip", digestOf([]byte("x")) + ".new"}
	for _, name := range leftovers {
		if err := os.MkdirAll(filepath.Join(f.host.sites, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	age := func(name string, by time.Duration) {
		at := f.clock.Add(-by)
		if err := os.Chtimes(filepath.Join(f.host.sites, name), at, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range append([]string{served, wanted, old}, leftovers...) {
		age(name, 2*siteKeepFor)
	}
	age(fresh, time.Minute) // sent a moment ago and not named yet

	f.host.pruneSites()

	var kept []string
	entries, err := os.ReadDir(f.host.sites)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		kept = append(kept, entry.Name())
	}
	want := []string{served, wanted, fresh}
	slices.Sort(want)
	if !slices.Equal(kept, want) {
		t.Fatalf("the sites folder holds %v, want the site served, the one wanted and the one just sent: %v", kept, want)
	}
}

// The loop sweeps on each pass, so a site that nothing names goes without anyone asking for it.
func TestRunSweepsTheSitesNobodyNames(t *testing.T) {
	f := newDecoyFixture(t)
	stale := f.store(map[string]string{"index.html": "<html>stale</html>"})
	at := f.clock.Add(-2 * siteKeepFor)
	if err := os.Chtimes(f.host.sitePath(stale), at, at); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		f.host.Run(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()
	for deadline := time.Now().Add(5 * time.Second); f.host.holdsSite(stale); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the loop did not sweep a site that nothing names")
		}
	}
}
