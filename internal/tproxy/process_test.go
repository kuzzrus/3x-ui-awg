package tproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Regression for a real production bug: binaryPath (tproxyServerBinaryPath/
// mtproxyBinaryPath) is relative to the panel's own working directory
// ("bin/...", config.GetBinFolderPath's default), and Start also sets
// cmd.Dir to a relative path (dir(), "bin/tproxy"). Once cmd.Dir is set, a
// relative cmd.Path resolves against *that*, not the caller's cwd -- so
// unfixed, every real deployment (XUI_BIN_FOLDER unset, the normal case)
// looked for the nonexistent "bin/tproxy/bin/mtproxy-linux-amd64" on every
// single reconcile tick, forever, with the misleadingly plain error
// "fork/exec bin/mtproxy-linux-amd64: no such file or directory" even
// though the real binary sat one directory up the whole time. The real
// binary's own file permissions, presence, and checkPlatform were all fine;
// nothing about this bug was visible from the filesystem side, only from
// tracing exactly how exec.Cmd resolves a relative Path once Dir is set.
//
// installFakeBinaries elsewhere in this package sets XUI_BIN_FOLDER to an
// *absolute* t.TempDir(), which makes binaryPath already absolute and could
// never have exercised this path -- this test deliberately keeps it
// relative, matching every real install.
func TestStartResolvesRelativeBinaryPathAgainstCallerCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec of a #!/bin/sh script needs a Unix-like OS")
	}

	root := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	// Mirrors the real layout: the binary sits directly under bin/, cmd.Dir
	// (dir()) is the nested bin/tproxy/ -- the two must never be conflated.
	if err := os.MkdirAll("bin/tproxy", 0o700); err != nil {
		t.Fatalf("mkdir bin/tproxy: %v", err)
	}
	if err := os.WriteFile("bin/fake-binary", []byte("#!/bin/sh\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	p := newChildProcess("bin/fake-binary", nil, "127.0.0.1:1", "regression", 0)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v -- binary exists at %s/bin/fake-binary, cmd.Dir is bin/tproxy", err, root)
	}
}

// Regression for a real production bug: mtproxy-linux-amd64 is statically
// linked (release.yml requires it for Alpine/CentOS 7 support), and its own
// change_user_group unconditionally calls getpwnam whenever it sees euid 0 --
// glibc's NSS resolves that through dlopen, which a static binary can never
// satisfy, so the real engine crashed and exit(1)'d on every single launch
// as root (see privdrop.go). This only proves the mechanism Start uses to
// avoid ever exposing euid 0 to a dropToGID child in the first place: it
// can't exec the real engine, so it has the fake binary report its own
// uid/gid instead of trusting SysProcAttr silently having no effect.
//
// Also a regression for the shared-UID isolation bug (privdrop.go's
// mtproxyEgressGID doc comment, firewall.go's egressRedirect doc comment):
// this asserts the child's GID is the exact per-inbound value passed to
// newChildProcess, not just "some non-zero value" -- a test that only
// checked UID would have passed even with the old, broken shared-uid-only
// design, since UID was never the part that changed.
func TestStartDropsPrivilegesForDropPrivilegesChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("exec of a #!/bin/sh script needs a Unix-like OS")
	}
	if os.Geteuid() != 0 {
		t.Skip("only root can drop privileges for a child -- this package's own production code skips the same way")
	}
	wantUID, _, err := unprivilegedIDs()
	if err != nil {
		t.Skipf("no %s account on this system: %v", mtproxyUser, err)
	}
	wantGID := mtproxyEgressGID(42)

	// Not t.TempDir(): see installFakeBinaries in manager_test.go for why its
	// hidden per-test parent directory defeats a chmod on the returned leaf.
	root, err := os.MkdirTemp("", "tproxy-dropcred-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatalf("chmod %s: %v", root, err)
	}
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	if err := os.MkdirAll("bin/tproxy", 0o755); err != nil {
		t.Fatalf("mkdir bin/tproxy: %v", err)
	}
	idFile := filepath.Join(root, "ids")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' \"$(id -u)\" \"$(id -g)\" > %s\n", idFile)
	if err := os.WriteFile("bin/fake-binary", []byte(script), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	p := newChildProcess("bin/fake-binary", nil, "127.0.0.1:1", "regression", wantGID)
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Stop() })

	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if got, err = os.ReadFile(idFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("read %s: %v (result: %s)", idFile, err, p.GetResult())
	}
	fields := strings.Fields(string(got))
	if len(fields) != 2 {
		t.Fatalf("ids file = %q, want \"<uid> <gid>\"", string(got))
	}
	gotUID, gotGID := fields[0], fields[1]
	if gotUID == "0" || gotGID == "0" {
		t.Fatal("child ran as uid/gid 0 -- privilege drop had no effect")
	}
	if want := strconv.FormatUint(uint64(wantUID), 10); gotUID != want {
		t.Errorf("child ran as uid %s, want %s (%s)", gotUID, want, mtproxyUser)
	}
	if want := strconv.FormatUint(uint64(wantGID), 10); gotGID != want {
		t.Errorf("child ran as gid %s, want %s (mtproxyEgressGID(42), not %s's own shared gid) -- this is the exact isolation the GID-based redirect depends on", gotGID, want, mtproxyUser)
	}
}
