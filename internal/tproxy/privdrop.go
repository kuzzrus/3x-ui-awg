package tproxy

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// mtproxyUser is the account the MTProxy engine child runs as instead of
// root. mtproxy-linux-amd64 is built statically (release.yml overrides its
// Makefile's own -rdynamic with -static so it can exec at all on Alpine,
// which has no glibc, and on CentOS 7's glibc 2.17), and a real production
// crash traced its exit(1)+backtrace back -- via nm against the deployed,
// unstripped binary -- to engine_init -> change_user_group -> getpwnam.
// change_user_group runs that lookup unconditionally whenever geteuid() is
// 0, substituting a default username only when none was passed on the
// command line; there is no argument that skips the call. glibc's NSS
// resolves getpwnam through dlopen (loading nss_files/nss_compat at
// runtime), which a fully static binary can never satisfy -- this is a
// long-documented glibc limitation, not something fixable by linker flags
// or by pre-creating the account it happens to look up. The only reliable
// fix is to never let the child observe euid 0 in the first place:
// change_user_group's whole privileged-account branch is skipped when it
// isn't root to begin with.
const mtproxyUser = "nobody"

// dirPerm is dir()'s mode: owner (root) gets full access, and mtproxyUser
// gets bare traversal (the "x" other-bit) so it can open the two files
// chownForMTProxy hands it by exact path, without the "r" other-bit that
// would let it (or any other local account) list the directory and see
// every other file tproxy-server's root-only config lives next to.
const dirPerm = 0o701

// unprivilegedIDs resolves mtproxyUser once; both process.go's Start (to set
// the child's own credential) and telegramconfig.go (to chown the two files
// that child must itself open by path) need the identical uid/gid.
func unprivilegedIDs() (uid, gid uint32, err error) {
	u, err := user.Lookup(mtproxyUser)
	if err != nil {
		return 0, 0, fmt.Errorf("looking up user %q: %w", mtproxyUser, err)
	}
	uid64, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("user %q has non-numeric uid %q: %w", mtproxyUser, u.Uid, err)
	}
	gid64, err := strconv.ParseUint(u.Gid, 10, 32)
	if err != nil {
		return 0, 0, fmt.Errorf("user %q has non-numeric gid %q: %w", mtproxyUser, u.Gid, err)
	}
	return uint32(uid64), uint32(gid64), nil
}

// chownForMTProxy gives mtproxyUser ownership of path, one of the two files
// (proxy-secret, proxy-multi.conf) the engine child opens directly by name
// after Start drops it to that same account. A no-op when not root: outside
// a real deployment (tests) mtproxyUser need not even exist, and Start's own
// privilege drop is equally skipped there, so no chown is needed either.
func chownForMTProxy(path string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	uid, gid, err := unprivilegedIDs()
	if err != nil {
		return err
	}
	if err := os.Chown(path, int(uid), int(gid)); err != nil {
		return fmt.Errorf("cannot chown %s to %s: %w", path, mtproxyUser, err)
	}
	return nil
}
