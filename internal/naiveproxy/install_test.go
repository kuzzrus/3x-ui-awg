package naiveproxy

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ulikunitz/xz"
)

// Pins the exact value, not just its shape: a 64-character-but-wrong string
// would pass a length-only check and still fail every real Install.
func TestReleaseSHA256Value(t *testing.T) {
	// Independently retyped from the release asset's own published digest,
	// not copied from the production constant, so the two must agree on purpose.
	const want = "c273dc07dd79c5919f97639cbdcb559d40ce90475dbd8aac80641b0a8c8065ff"
	if releaseSHA256 != want {
		t.Fatalf("releaseSHA256 = %q, want %q", releaseSHA256, want)
	}
	got, err := hex.DecodeString(releaseSHA256)
	if err != nil {
		t.Fatalf("releaseSHA256 does not decode as hex: %v", err)
	}
	if len(got) != sha256.Size {
		t.Fatalf("releaseSHA256 decodes to %d bytes, want %d", len(got), sha256.Size)
	}
}

func TestCheckPlatform(t *testing.T) {
	for _, tc := range []struct {
		goos, goarch string
		wantErr      bool
	}{
		{goos: "linux", goarch: "amd64"},
		{goos: "linux", goarch: "arm64", wantErr: true},
		{goos: "windows", goarch: "amd64", wantErr: true},
		{goos: "darwin", goarch: "arm64", wantErr: true},
	} {
		t.Run(tc.goos+"/"+tc.goarch, func(t *testing.T) {
			err := checkPlatform(tc.goos, tc.goarch)
			if tc.wantErr && err == nil {
				t.Fatalf("checkPlatform(%s, %s) = nil, want an error", tc.goos, tc.goarch)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkPlatform(%s, %s): %v", tc.goos, tc.goarch, err)
			}
		})
	}
}

// buildTestArchive writes a minimal, real xz-compressed tar with one entry,
// so extractBinary exercises its real decompression/detar path end to end.
func buildTestArchive(t *testing.T, entryName, content string) []byte {
	t.Helper()
	var tarBuf bytes.Buffer
	tw := tar.NewWriter(&tarBuf)
	hdr := &tar.Header{Name: entryName, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatalf("tar WriteHeader: %v", err)
	}
	if _, err := tw.Write([]byte(content)); err != nil {
		t.Fatalf("tar Write: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}

	var xzBuf bytes.Buffer
	xw, err := xz.NewWriter(&xzBuf)
	if err != nil {
		t.Fatalf("xz.NewWriter: %v", err)
	}
	if _, err := xw.Write(tarBuf.Bytes()); err != nil {
		t.Fatalf("xz Write: %v", err)
	}
	if err := xw.Close(); err != nil {
		t.Fatalf("xz Close: %v", err)
	}
	return xzBuf.Bytes()
}

func TestExtractBinaryFindsTheRealEntry(t *testing.T) {
	const content = "pretend-this-is-the-caddy-binary"
	archive := buildTestArchive(t, archiveEntryName, content)
	dst := filepath.Join(t.TempDir(), "out")

	if err := extractBinary(bytes.NewReader(archive), dst); err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != content {
		t.Errorf("extracted content = %q, want %q", got, content)
	}
}

// A tar built as "tar -cJf … ./caddy-forwardproxy-naive" (a common invocation
// shape) writes entry names with a leading "./" -- extractBinary must still
// find the binary, not reject an otherwise-valid archive as empty.
func TestExtractBinaryToleratesADotSlashPrefix(t *testing.T) {
	const content = "pretend-this-is-the-caddy-binary"
	archive := buildTestArchive(t, "./"+archiveEntryName, content)
	dst := filepath.Join(t.TempDir(), "out")

	if err := extractBinary(bytes.NewReader(archive), dst); err != nil {
		t.Fatalf("extractBinary with a ./-prefixed entry name: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("reading extracted file: %v", err)
	}
	if string(got) != content {
		t.Errorf("extracted content = %q, want %q", got, content)
	}
}

func TestExtractBinaryRejectsMissingEntry(t *testing.T) {
	archive := buildTestArchive(t, "some/other/file", "irrelevant")
	dst := filepath.Join(t.TempDir(), "out")

	err := extractBinary(bytes.NewReader(archive), dst)
	if err == nil {
		t.Fatal("extractBinary with no matching entry returned nil error")
	}
	if !strings.Contains(err.Error(), "no "+archiveEntryName+" entry") {
		t.Errorf("extractBinary error = %q, want it to name the missing entry", err.Error())
	}
}

func TestDownloadArchiveVerifiesDigest(t *testing.T) {
	const body = "pretend-this-is-the-naiveproxy-archive"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	realURL := downloadURL
	downloadURL = srv.URL
	t.Cleanup(func() { downloadURL = realURL })

	sum := sha256.Sum256([]byte(body))
	dst, err := os.CreateTemp(t.TempDir(), "archive-*.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	if err := downloadArchive(context.Background(), srv.Client(), sum[:], dst); err != nil {
		t.Fatalf("downloadArchive with the correct digest: %v", err)
	}
	if _, err := dst.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("downloaded content = %q, want %q", got, body)
	}
}

func TestDownloadArchiveRejectsWrongDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("actual content"))
	}))
	t.Cleanup(srv.Close)

	realURL := downloadURL
	downloadURL = srv.URL
	t.Cleanup(func() { downloadURL = realURL })

	wrong := sha256.Sum256([]byte("not the actual content"))
	dst, err := os.CreateTemp(t.TempDir(), "archive-*.tar.xz")
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()

	err = downloadArchive(context.Background(), srv.Client(), wrong[:], dst)
	if err == nil {
		t.Fatal("downloadArchive with a wrong digest returned nil error")
	}
	if !strings.Contains(err.Error(), "checksum verification") {
		t.Errorf("downloadArchive error = %q, want it to mention checksum verification", err.Error())
	}
}

// refusingTransport fails t if Install ever actually reaches the network --
// a stronger guard than a client with no timeout, which would just make a
// real request (and pass) if the already-installed check ever regressed.
type refusingTransport struct{ t *testing.T }

func (rt refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	rt.t.Error("Install reached the network with a binary already present")
	return nil, errors.New("network disabled in this test")
}

// Regression guard: Install must never touch the network once a binary is
// already present.
func TestInstallSkipsDownloadWhenAlreadyInstalled(t *testing.T) {
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

	client := &http.Client{Transport: refusingTransport{t}}
	if err := Install(context.Background(), client); err != nil {
		t.Fatalf("Install with an already-present binary: %v", err)
	}
}

func TestUninstallRemovesTheDirectory(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())

	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BinPath(), []byte("x"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := Uninstall(); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if IsInstalled() {
		t.Error("IsInstalled() = true after Uninstall")
	}
	if _, err := os.Stat(Dir()); !os.IsNotExist(err) {
		t.Errorf("Dir() still exists after Uninstall: %v", err)
	}
}

// installedEngine puts body at BinPath(), in a bin folder of its own, as an earlier panel version left it. A release
// other than "" is written down next to it.
func installedEngine(t *testing.T, body, release string) {
	t.Helper()
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BinPath(), []byte(body), 0o750); err != nil {
		t.Fatal(err)
	}
	if release != "" {
		if err := os.WriteFile(releasePath(), []byte(release+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func readEngine(t *testing.T) string {
	t.Helper()
	got, err := os.ReadFile(BinPath())
	if err != nil {
		t.Fatalf("reading the installed engine: %v", err)
	}
	return string(got)
}

// otherDigest is a checksum that no archive of these tests has.
var otherDigest = strings.Repeat("ab", sha256.Size)

// serveEngine answers every download with the release archive that holds engine, and returns the count of the
// requests and the checksum of that archive.
func serveEngine(t *testing.T, engine string) (*atomic.Int32, string) {
	t.Helper()
	archive := buildTestArchive(t, archiveEntryName, engine)
	sum := sha256.Sum256(archive)
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	realURL := downloadURL
	downloadURL = srv.URL
	t.Cleanup(func() { downloadURL = realURL })
	return &requests, hex.EncodeToString(sum[:])
}

func TestUpgradeReplacesAnEngineOfAnotherRelease(t *testing.T) {
	for name, release := range map[string]string{
		"none written down":   "",
		"another one written": otherDigest,
	} {
		t.Run(name, func(t *testing.T) {
			installedEngine(t, "the engine of an earlier panel version", release)
			requests, digest := serveEngine(t, "the pinned engine")

			replaced, err := upgradeTo(context.Background(), http.DefaultClient, "linux", "amd64", digest)

			if err != nil || !replaced {
				t.Fatalf("upgradeTo = %v, %v, want a replacement", replaced, err)
			}
			if got := readEngine(t); got != "the pinned engine" {
				t.Errorf("the installed engine is %q, want the pinned one", got)
			}
			if got := installedRelease(); got != digest {
				t.Errorf("the release written down is %q, want %q", got, digest)
			}
			if info, err := os.Stat(BinPath()); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0) {
				t.Errorf("the replacement is not executable (%v, %v)", info, err)
			}
			for _, leftover := range []string{BinPath() + ".new", BinPath() + ".tar.xz"} {
				if _, err := os.Stat(leftover); !os.IsNotExist(err) {
					t.Errorf("%s is still there (%v)", leftover, err)
				}
			}
			if n := requests.Load(); n != 1 {
				t.Errorf("%d downloads, want 1", n)
			}
		})
	}
}

func TestUpgradeLeavesAnEngineOfThePinnedReleaseAlone(t *testing.T) {
	requests, digest := serveEngine(t, "the pinned engine")
	installedEngine(t, "the pinned engine", digest)

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "linux", "amd64", digest)

	if err != nil || replaced {
		t.Fatalf("upgradeTo = %v, %v, want nothing to do", replaced, err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d downloads, want none for the release that is the pinned one", n)
	}
}

func TestUpgradeDoesNothingWithoutAnEngine(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	requests, digest := serveEngine(t, "the pinned engine")

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "linux", "amd64", digest)

	if err != nil || replaced {
		t.Fatalf("upgradeTo = %v, %v, want nothing to do", replaced, err)
	}
	if IsInstalled() || requests.Load() != 0 {
		t.Errorf("an upgrade installed something (%v) or downloaded (%d), it is no install", IsInstalled(), requests.Load())
	}
}

// Where no build is published the engine is the admin's own, not an old one of the panel's to be put right.
func TestUpgradeLeavesAnEngineAloneWhereNoBuildIsPublished(t *testing.T) {
	installedEngine(t, "an engine put there by hand", "")
	requests, digest := serveEngine(t, "the pinned engine")

	replaced, err := upgradeTo(context.Background(), http.DefaultClient, "linux", "arm64", digest)

	if err != nil || replaced {
		t.Fatalf("upgradeTo = %v, %v, want nothing to do where no build is published", replaced, err)
	}
	if got := readEngine(t); got != "an engine put there by hand" {
		t.Errorf("the installed engine is %q, want the admin's own", got)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d downloads where no build is published, want none", n)
	}
}

func TestUpgradeKeepsTheInstalledEngineWhenTheDownloadFails(t *testing.T) {
	for name, serve := range map[string]func(t *testing.T){
		"the host refuses": func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(srv.Close)
			realURL := downloadURL
			downloadURL = srv.URL
			t.Cleanup(func() { downloadURL = realURL })
		},
		"it is another archive": func(t *testing.T) { serveEngine(t, "not the pinned engine") },
	} {
		t.Run(name, func(t *testing.T) {
			installedEngine(t, "the engine of an earlier panel version", "")
			serve(t)

			replaced, err := upgradeTo(context.Background(), http.DefaultClient, "linux", "amd64", otherDigest)

			if err == nil || replaced {
				t.Fatalf("upgradeTo = %v, %v, want the failure", replaced, err)
			}
			if got := readEngine(t); got != "the engine of an earlier panel version" {
				t.Errorf("the installed engine is %q after a failed download, want it kept", got)
			}
			if got := installedRelease(); got != "" {
				t.Errorf("the release written down is %q after a failed download, want it as it was", got)
			}
			for _, leftover := range []string{BinPath() + ".new", BinPath() + ".tar.xz"} {
				if _, err := os.Stat(leftover); !os.IsNotExist(err) {
					t.Errorf("%s is still there (%v)", leftover, err)
				}
			}
		})
	}
}

// A first install writes the release down too, so that the next start does not fetch the engine again.
func TestFetchWritesDownTheReleaseOfAFirstInstall(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	_, digest := serveEngine(t, "the pinned engine")

	installMu.Lock()
	err := fetchLocked(context.Background(), http.DefaultClient, digest)
	installMu.Unlock()

	if err != nil {
		t.Fatalf("fetchLocked: %v", err)
	}
	if got := installedRelease(); got != digest {
		t.Errorf("the release written down is %q, want %q", got, digest)
	}
	if got := readEngine(t); got != "the pinned engine" {
		t.Errorf("the installed engine is %q, want the pinned one", got)
	}
}

// A second upgrade that starts during the first one's download looks at the engine once that has been replaced.
func TestTwoUpgradesAtOnceFetchTheEngineOnce(t *testing.T) {
	installedEngine(t, "the engine of an earlier panel version", "")
	archive := buildTestArchive(t, archiveEntryName, "the pinned engine")
	sum := sha256.Sum256(archive)
	digest := hex.EncodeToString(sum[:])
	var requests atomic.Int32
	arrived, hold := make(chan struct{}, 2), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		arrived <- struct{}{}
		<-hold
		_, _ = w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	realURL := downloadURL
	downloadURL = srv.URL
	t.Cleanup(func() { downloadURL = realURL })

	var (
		wg       sync.WaitGroup
		replaced [2]bool
		errs     [2]error
	)
	upgrade := func(i int) {
		defer wg.Done()
		replaced[i], errs[i] = upgradeTo(context.Background(), http.DefaultClient, "linux", "amd64", digest)
	}
	wg.Add(1)
	go upgrade(0)
	select {
	case <-arrived:
	case <-time.After(10 * time.Second):
		t.Fatal("the first download never started")
	}
	wg.Add(1)
	go upgrade(1)
	time.Sleep(50 * time.Millisecond) // long enough for a second download to start, were one going to
	close(hold)
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("upgradeTo errors = %v, %v", errs[0], errs[1])
	}
	if replaced[0] == replaced[1] {
		t.Errorf("replaced = %v, want exactly one of the two upgrades to have put the engine there", replaced)
	}
	if n := requests.Load(); n != 1 {
		t.Errorf("%d downloads for two upgrades at once, want 1", n)
	}
	if got := readEngine(t); got != "the pinned engine" {
		t.Errorf("the installed engine is %q, want the pinned one", got)
	}
}
