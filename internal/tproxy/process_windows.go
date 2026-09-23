//go:build windows

package tproxy

import "syscall"

// credentialSysProcAttr is unreachable at runtime: os.Geteuid() is always -1
// on Windows, so Start's "if p.dropPrivileges && os.Geteuid() == 0" branch
// that calls this can never be true here, and checkPlatform independently
// refuses every OS but linux before any of this package's real work runs.
// This stub exists purely so x-ui.exe's own Windows build -- which compiles
// this package even though it can never use it -- type-checks; see
// process_unix.go for the real implementation.
func credentialSysProcAttr(uid, gid uint32) *syscall.SysProcAttr {
	return nil
}
