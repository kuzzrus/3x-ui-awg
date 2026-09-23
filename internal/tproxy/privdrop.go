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

// mtproxyEgressGIDBase is added to an inbound's own id to build that
// engine's process GID -- see mtproxyEgressGID. Chosen far above any real
// system uid/gid range (those never approach even six digits in practice on
// any distro this fork targets) so it can never collide with a real group,
// without needing one to actually exist in /etc/group: Linux's setgid(2)
// accepts any numeric gid_t, real group database entry or not -- confirmed
// empirically, not assumed.
const mtproxyEgressGIDBase = 2_000_000_000

// mtproxyEgressGID computes the per-inbound process GID every MTProxy
// engine child runs under, instead of mtproxyUser's own shared group.
//
// The reason this exists at all: every engine child's UID is the same
// (mtproxyUser, "nobody") regardless of inbound, since that identity only
// ever needs to prove "I'm the sandboxed tproxy engine account" for file
// ownership (chownForMTProxy) and dir()'s traversal bit -- neither cares
// which inbound. But firewall.go's egress_redirect nftables rule has a
// completely different job: distinguishing *one specific inbound's* own
// engine process from every other one on the box, including every other
// tproxy inbound's engine, so that enabling routeThroughXray on inbound A
// can never redirect inbound B's traffic too. A shared UID-only match
// (skuid) cannot express that distinction at all -- confirmed live, on a
// real deployment, before this function existed: enabling routing on one
// test inbound silently redirected an unrelated, non-routed inbound's own
// engine as well, since nftables has no notion of "inbound" and can only
// ever see the process's credentials. Giving every engine its own process
// GID, independent of whether that inbound even uses routeThroughXray,
// gives the nftables rule (meta skgid, not meta skuid) something genuinely
// unique to match -- live-verified against a real redirect+SO_ORIGINAL_DST
// setup: two children sharing one UID but carrying different GIDs (one
// arbitrary and never present in /etc/group) redirected and left alone
// respectively, exactly as their own individual GID dictated.
func mtproxyEgressGID(inboundID int) uint32 {
	return mtproxyEgressGIDBase + uint32(inboundID)
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
