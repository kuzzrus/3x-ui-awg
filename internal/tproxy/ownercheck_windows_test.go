//go:build windows

package tproxy

import "testing"

// fileOwner is unreachable: every caller runs behind an os.Geteuid() != 0
// skip, and os.Geteuid() is always -1 on Windows. See ownercheck_unix_test.go.
func fileOwner(t *testing.T, path string) (uid, gid uint32) {
	t.Helper()
	t.Fatal("fileOwner is unreachable on windows -- callers must skip on os.Geteuid() != 0 first")
	return 0, 0
}
