//go:build !windows

package tproxy

import "syscall"

// credentialSysProcAttr builds the SysProcAttr Start uses to drop a child to
// an unprivileged uid/gid before exec -- see privdrop.go. Split into its own
// build-tagged file because syscall.Credential (and the Credential field of
// syscall.SysProcAttr itself) is Unix-only: Windows' SysProcAttr has an
// entirely different shape (Token, not Uid/Gid). checkPlatform already
// confines every real use of this package to linux/amd64, but x-ui.exe's own
// Windows build still compiles this package, so it needs something that
// type-checks there too -- see process_windows.go.
func credentialSysProcAttr(uid, gid uint32) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: gid}}
}
