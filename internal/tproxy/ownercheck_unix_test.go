//go:build !windows

package tproxy

import (
	"os"
	"syscall"
	"testing"
)

// fileOwner reads path's owning uid/gid. Split out from telegramconfig_test.go
// the same way process_unix.go/process_windows.go split credentialSysProcAttr:
// syscall.Stat_t is Unix-only, and every caller here already runs behind an
// os.Geteuid() != 0 skip that is unconditionally true on Windows -- but that
// skip is a runtime check, not a build constraint, so the type still has to
// exist on every platform this package's tests compile on.
func fileOwner(t *testing.T, path string) (uid, gid uint32) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat %s: not a syscall.Stat_t on this platform", path)
	}
	return sys.Uid, sys.Gid
}
