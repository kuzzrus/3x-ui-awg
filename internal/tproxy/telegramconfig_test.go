package tproxy

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestValidateProxyMultiConf(t *testing.T) {
	valid := strings.Repeat("# comment padding to clear the byte floor\n", 5) + "default 1.2.3.4:443\nproxy_for 1 1.2.3.4:443\n"
	cases := []struct {
		name            string
		body            string
		wantErrContains string // empty means no error
	}{
		{"valid", valid, ""},
		{"too short", "default 1.2.3.4:443\nproxy_for 1 1.2.3.4:443\n", "at least"},
		{"missing default line", strings.Repeat("#\n", 60) + "proxy_for 1 1.2.3.4:443\n", `"default "`},
		{"missing proxy_for line", strings.Repeat("#\n", 60) + "default 1.2.3.4:443\n", `"proxy_for "`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateProxyMultiConf([]byte(c.body))
			if c.wantErrContains == "" {
				if err != nil {
					t.Errorf("validateProxyMultiConf(%q) = %v, want no error", c.name, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErrContains) {
				t.Errorf("validateProxyMultiConf(%q) error = %v, want it to contain %q", c.name, err, c.wantErrContains)
			}
		})
	}
}

// telegramTestServers points proxySecretURL/proxyMultiConfURL at local
// httptest handlers for the duration of one test, restoring both afterward.
func telegramTestServers(t *testing.T, secret []byte, conf []byte) {
	t.Helper()
	secretSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(secret)
	}))
	confSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(conf)
	}))
	t.Cleanup(func() {
		secretSrv.Close()
		confSrv.Close()
	})
	origSecret, origConf := proxySecretURL, proxyMultiConfURL
	proxySecretURL, proxyMultiConfURL = secretSrv.URL, confSrv.URL
	t.Cleanup(func() { proxySecretURL, proxyMultiConfURL = origSecret, origConf })
}

func validMultiConf() []byte {
	return []byte(strings.Repeat("# padding\n", 10) + "default 1.2.3.4:443\nproxy_for 1 1.2.3.4:443\n")
}

func TestEnsureTelegramConfigFilesFetchesOnlyMissing(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	secret := bytes.Repeat([]byte{0x11}, proxySecretSize)
	telegramTestServers(t, secret, validMultiConf())

	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err != nil {
		t.Fatalf("EnsureTelegramConfigFiles: %v", err)
	}
	got, err := os.ReadFile(proxySecretPath())
	if err != nil {
		t.Fatalf("read proxy-secret: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Error("proxy-secret content mismatch")
	}
	if !isRegularFile(proxyMultiConfPath()) {
		t.Error("proxy-multi.conf was not written")
	}

	// Pre-existing files must never be overwritten by Ensure -- only Refresh does that.
	if err := os.WriteFile(proxySecretPath(), []byte("do-not-touch"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err != nil {
		t.Fatalf("second EnsureTelegramConfigFiles: %v", err)
	}
	got, err = os.ReadFile(proxySecretPath())
	if err != nil {
		t.Fatalf("read proxy-secret: %v", err)
	}
	if string(got) != "do-not-touch" {
		t.Error("EnsureTelegramConfigFiles overwrote an already-present file")
	}
}

func TestEnsureTelegramConfigFilesRejectsBadSecretSize(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	telegramTestServers(t, []byte("too-short"), validMultiConf())
	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err == nil {
		t.Fatal("EnsureTelegramConfigFiles accepted a proxy-secret of the wrong size")
	}
	if isRegularFile(proxySecretPath()) {
		t.Error("a rejected download must not be left on disk")
	}
}

func TestRefreshTelegramConfigReportsChange(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	first := validMultiConf()
	telegramTestServers(t, bytes.Repeat([]byte{0x11}, proxySecretSize), first)
	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err != nil {
		t.Fatalf("EnsureTelegramConfigFiles: %v", err)
	}

	changed, err := RefreshTelegramConfig(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("RefreshTelegramConfig (same content): %v", err)
	}
	if changed {
		t.Error("RefreshTelegramConfig reported a change when the content is identical")
	}

	second := []byte(strings.Repeat("# padding\n", 10) + "default 5.6.7.8:443\nproxy_for 1 5.6.7.8:443\n")
	telegramTestServers(t, bytes.Repeat([]byte{0x11}, proxySecretSize), second)
	changed, err = RefreshTelegramConfig(t.Context(), http.DefaultClient)
	if err != nil {
		t.Fatalf("RefreshTelegramConfig (changed content): %v", err)
	}
	if !changed {
		t.Error("RefreshTelegramConfig did not report a real content change")
	}
	got, err := os.ReadFile(proxyMultiConfPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Error("RefreshTelegramConfig did not persist the new content")
	}
}

// Regression for a real production bug, the other half of
// TestStartDropsPrivilegesForDropPrivilegesChild in process_test.go: once
// Start drops the MTProxy engine child to mtproxyUser (privdrop.go), that
// account must actually be able to open the two files it reads by path --
// proxy-secret and proxy-multi.conf, written here as root 0600 -- and reach
// them through dir(), which used to be 0700 (root-only, no "x" for anyone
// else, blocking traversal regardless of the files' own ownership).
func TestEnsureTelegramConfigFilesReadableByMTProxyUser(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	telegramTestServers(t, bytes.Repeat([]byte{0x11}, proxySecretSize), validMultiConf())
	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err != nil {
		t.Fatalf("EnsureTelegramConfigFiles: %v", err)
	}

	info, err := os.Stat(dir())
	if err != nil {
		t.Fatalf("stat %s: %v", dir(), err)
	}
	if info.Mode().Perm()&0o001 == 0 {
		t.Errorf("dir() is %o, want the other-execute bit set so a non-owner account can traverse it", info.Mode().Perm())
	}

	if os.Geteuid() != 0 {
		t.Skip("chownForMTProxy is a no-op unless root; ownership itself is only meaningful there")
	}
	wantUID, wantGID, err := unprivilegedIDs()
	if err != nil {
		t.Skipf("no %s account on this system: %v", mtproxyUser, err)
	}
	for _, path := range []string{proxySecretPath(), proxyMultiConfPath()} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("stat %s: not a syscall.Stat_t on this platform", path)
		}
		if sys.Uid != wantUID || sys.Gid != wantGID {
			t.Errorf("%s owned by %d:%d, want %d:%d (%s)", path, sys.Uid, sys.Gid, wantUID, wantGID, mtproxyUser)
		}
	}
}

// Regression for a real production bug found deploying the fix above: a box
// that already had proxy-secret/proxy-multi.conf on disk from before
// mtproxyUser existed in this codebase -- any real upgrade -- hits
// isRegularFile true for both, so EnsureTelegramConfigFiles' "only fetch
// what's missing" branches never run and, before this test, never chowned
// either file either. The engine then failed with a legible but still wrong
// "cannot re-read config file ...: Permission denied" on every restart,
// forever, since nothing ever revisited a file it considered already
// provisioned.
func TestEnsureTelegramConfigFilesFixesOwnershipOnPreExistingFiles(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chownForMTProxy is a no-op unless root; ownership itself is only meaningful there")
	}
	wantUID, wantGID, err := unprivilegedIDs()
	if err != nil {
		t.Skipf("no %s account on this system: %v", mtproxyUser, err)
	}
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	// Mirrors a pre-upgrade install: files present, root-owned, before this
	// package ever called chownForMTProxy on anything.
	if err := os.MkdirAll(dir(), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir(), err)
	}
	if err := os.WriteFile(proxySecretPath(), bytes.Repeat([]byte{0x22}, proxySecretSize), 0o600); err != nil {
		t.Fatalf("seed proxy-secret: %v", err)
	}
	if err := os.WriteFile(proxyMultiConfPath(), validMultiConf(), 0o600); err != nil {
		t.Fatalf("seed proxy-multi.conf: %v", err)
	}

	// No telegramTestServers: neither URL should be hit, since both files
	// already exist -- if EnsureTelegramConfigFiles regresses back to
	// chowning only inside the fetch branches, this call would panic on a
	// nil default transport hitting the real internet instead of silently
	// passing, which is a deliberate tripwire, not an accident.
	if err := EnsureTelegramConfigFiles(t.Context(), http.DefaultClient); err != nil {
		t.Fatalf("EnsureTelegramConfigFiles: %v", err)
	}

	for _, path := range []string{proxySecretPath(), proxyMultiConfPath()} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		sys, ok := st.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("stat %s: not a syscall.Stat_t on this platform", path)
		}
		if sys.Uid != wantUID || sys.Gid != wantGID {
			t.Errorf("pre-existing %s still owned by %d:%d after EnsureTelegramConfigFiles, want %d:%d (%s)", path, sys.Uid, sys.Gid, wantUID, wantGID, mtproxyUser)
		}
	}
}
