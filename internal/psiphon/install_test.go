package psiphon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func sha256Sum(t *testing.T, s string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}

// Pins the exact value, not just its shape: a 64-character-but-wrong string
// would pass a length-only check and still fail every real Install.
func TestReleaseSHA256Value(t *testing.T) {
	// Independently retyped from `curl -sL <pinned URL> | sha256sum`, not
	// copied from the production constant, so the two must agree on purpose.
	const want = "47f8956f3f3cf9813d4cbee4665adc99b1f8ffa788c13dc5e03e824cc29217b0"
	if releaseSHA256 != want {
		t.Fatalf("releaseSHA256 = %q, want %q", releaseSHA256, want)
	}
	got, err := hex.DecodeString(releaseSHA256)
	if err != nil {
		t.Fatalf("releaseSHA256 does not decode as hex: %v", err)
	}
	if len(got) != 32 {
		t.Fatalf("releaseSHA256 decodes to %d bytes, want 32 (sha256.Size)", len(got))
	}
}

func TestHasDigest(t *testing.T) {
	const body = "pretend-this-is-the-psiphon-binary"
	path := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(path, []byte(body), 0o750); err != nil {
		t.Fatal(err)
	}
	right := hex.EncodeToString(sha256Sum(t, body))
	if !hasDigest(path, right) {
		t.Error("hasDigest = false for the digest of the file")
	}
	if hasDigest(path, hex.EncodeToString(sha256Sum(t, "another binary"))) {
		t.Error("hasDigest = true for the digest of another file")
	}
	if hasDigest(filepath.Join(t.TempDir(), "missing"), right) {
		t.Error("hasDigest = true for a file that is not there")
	}
	if MatchesPin() {
		t.Error("MatchesPin = true with no binary installed")
	}
}

func TestAssetPath(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		want         string
		wantErr      bool
	}{
		{goos: "linux", goarch: "amd64", want: "linux/psiphon-tunnel-core-x86_64"},
		{goos: "linux", goarch: "arm64", wantErr: true},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "darwin", goarch: "arm64", wantErr: true},
	} {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			got, err := assetPath(tc.goos, tc.goarch)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("assetPath returned %q, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("assetPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("assetPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDownloadBinaryVerifiesDigest(t *testing.T) {
	const body = "pretend-this-is-the-psiphon-binary"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	realURL := downloadURL
	downloadURL = func(string) string { return srv.URL }
	t.Cleanup(func() { downloadURL = realURL })

	sum := sha256Sum(t, body)
	dst := filepath.Join(t.TempDir(), "out")

	if err := downloadBinary(context.Background(), srv.Client(), "asset", sum, dst); err != nil {
		t.Fatalf("downloadBinary with the correct digest: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading downloaded file: %v", err)
	}
	if string(got) != body {
		t.Errorf("downloaded content = %q, want %q", got, body)
	}
}

// Install must seed the bundled default config even when the binary is
// already present and its own early return skips everything else --
// otherwise a pre-existing install (or one whose config Uninstall just
// wiped) never gets one.
func TestInstallSeedsConfigWhenAlreadyInstalled(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatalf("creating %s: %v", Dir(), err)
	}
	if err := os.WriteFile(BinPath(), []byte("pretend-binary"), 0o750); err != nil {
		t.Fatalf("seeding a fake already-installed binary: %v", err)
	}
	if !IsInstalled() {
		t.Fatal("IsInstalled() = false right after writing BinPath(), setup is broken")
	}
	if IsConfigured() {
		t.Fatal("IsConfigured() = true before Install ran, want a clean temp dir")
	}

	// A client that would fail any real request -- Install must never reach
	// the download path here, since IsInstalled() is already true.
	if err := Install(context.Background(), http.DefaultClient); err != nil {
		t.Fatalf("Install with an already-present binary: %v", err)
	}
	if !IsConfigured() {
		t.Error("IsConfigured() = false after Install, want the default config seeded")
	}
}

func TestDownloadBinaryRejectsWrongDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("actual content"))
	}))
	t.Cleanup(srv.Close)

	realURL := downloadURL
	downloadURL = func(string) string { return srv.URL }
	t.Cleanup(func() { downloadURL = realURL })

	wrong := sha256Sum(t, "not the actual content")
	dst := filepath.Join(t.TempDir(), "out")

	err := downloadBinary(context.Background(), srv.Client(), "asset", wrong, dst)
	if err == nil {
		t.Fatal("downloadBinary with a wrong digest returned nil error, want a checksum failure")
	}
	if !strings.Contains(err.Error(), "checksum verification") {
		t.Errorf("downloadBinary error = %q, want it to mention checksum verification", err.Error())
	}
}

// installedBuild puts body at BinPath(), in a bin folder of its own, as the build an earlier panel version downloaded.
func installedBuild(t *testing.T, body string) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BinPath(), []byte(body), 0o750); err != nil {
		t.Fatal(err)
	}
}

// pinnedBuildServer answers every download with status and, when that is 200, body; the count is of the requests.
func pinnedBuildServer(t *testing.T, status int, body string) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	realURL := downloadURL
	downloadURL = func(string) string { return srv.URL }
	t.Cleanup(func() { downloadURL = realURL })
	return &requests
}

func readBuild(t *testing.T) string {
	t.Helper()
	got, err := os.ReadFile(BinPath())
	if err != nil {
		t.Fatalf("reading the installed build: %v", err)
	}
	return string(got)
}

func TestUpgradeReplacesABuildThatIsNotThePinnedOne(t *testing.T) {
	installedBuild(t, "the build of an earlier panel version")
	requests := pinnedBuildServer(t, http.StatusOK, "the pinned build")
	digest := hex.EncodeToString(sha256Sum(t, "the pinned build"))

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "asset", digest)

	if err != nil || !replaced {
		t.Fatalf("upgradeTo = %v, %v, want a replacement", replaced, err)
	}
	if got := readBuild(t); got != "the pinned build" {
		t.Errorf("the installed build is %q, want the pinned one", got)
	}
	if info, err := os.Stat(BinPath()); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Errorf("the replacement is not executable (%v, %v)", info, err)
	}
	if _, err := os.Stat(BinPath() + ".new"); !os.IsNotExist(err) {
		t.Errorf("the staging file is still there (%v)", err)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d downloads, want 1", n)
	}
}

func TestUpgradeLeavesTheBuildThatIsThePinnedOneAlone(t *testing.T) {
	installedBuild(t, "the pinned build")
	requests := pinnedBuildServer(t, http.StatusOK, "the pinned build")
	digest := hex.EncodeToString(sha256Sum(t, "the pinned build"))

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "asset", digest)

	if err != nil || replaced {
		t.Fatalf("upgradeTo = %v, %v, want nothing to do", replaced, err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d downloads, want none for the build that is the pinned one", n)
	}
}

func TestUpgradeDoesNothingWithoutAnInstall(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	requests := pinnedBuildServer(t, http.StatusOK, "the pinned build")

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "asset", hex.EncodeToString(sha256Sum(t, "the pinned build")))

	if err != nil || replaced {
		t.Fatalf("upgradeTo = %v, %v, want nothing to do", replaced, err)
	}
	if IsInstalled() || requests.Load() != 0 {
		t.Errorf("an upgrade installed something (%v) or downloaded (%d), it is no install", IsInstalled(), requests.Load())
	}
}

func TestUpgradeKeepsTheInstalledBuildWhenTheDownloadFails(t *testing.T) {
	digest := hex.EncodeToString(sha256Sum(t, "the pinned build"))
	for name, serve := range map[string]func(t *testing.T){
		"the host refuses":   func(t *testing.T) { pinnedBuildServer(t, http.StatusInternalServerError, "") },
		"it is another file": func(t *testing.T) { pinnedBuildServer(t, http.StatusOK, "not the pinned build") },
	} {
		t.Run(name, func(t *testing.T) {
			installedBuild(t, "the build of an earlier panel version")
			serve(t)

			replaced, err := upgradeTo(context.Background(), http.DefaultClient, "asset", digest)

			if err == nil || replaced {
				t.Fatalf("upgradeTo = %v, %v, want the failure", replaced, err)
			}
			if got := readBuild(t); got != "the build of an earlier panel version" {
				t.Errorf("the installed build is %q after a failed download, want it kept", got)
			}
			if _, err := os.Stat(BinPath() + ".new"); !os.IsNotExist(err) {
				t.Errorf("the staging file is still there (%v)", err)
			}
		})
	}
}

func TestUpgradeRefusesAPinnedChecksumThatIsNotOne(t *testing.T) {
	installedBuild(t, "the build of an earlier panel version")
	requests := pinnedBuildServer(t, http.StatusOK, "the pinned build")

	for name, digest := range map[string]string{"not hex": "not-a-checksum", "too short": "abcd"} {
		t.Run(name, func(t *testing.T) {
			replaced, err := upgradeTo(context.Background(), http.DefaultClient, "asset", digest)

			if err == nil || !strings.Contains(err.Error(), "malformed pinned checksum") || replaced {
				t.Fatalf("upgradeTo = %v, %v, want the malformed checksum refused", replaced, err)
			}
		})
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d downloads for a checksum that cannot be met, want none", n)
	}
	if got := readBuild(t); got != "the build of an earlier panel version" {
		t.Errorf("the installed build is %q, want it kept", got)
	}
}
