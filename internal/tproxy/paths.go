package tproxy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
)

// checkPlatform rejects every host but the one release.yml builds both
// binaries for -- MTProxy's CRC32 hot path is real inline x86 asm.
func checkPlatform(goos, goarch string) error {
	if goos == "linux" && goarch == "amd64" {
		return nil
	}
	return fmt.Errorf("the Telegram web proxy is only available on linux/amd64 (this host is %s/%s)", goos, goarch)
}

// dir is where every file this package writes lives, matching the
// "sidecar owns a subdirectory of bin/" convention every other one uses.
// Absolute: Start sets a child's cmd.Dir to this same value, and every path
// built from it (serverConfigPath, profilesPath, ...) is handed to that
// child as a CLI argument. If either were left relative to
// config.GetBinFolderPath()'s default ("bin"), the child would resolve the
// argument against its OWN cwd -- which Start already pointed here -- and
// look for the directory doubled under itself (bin/tproxy/bin/tproxy/...),
// never finding it.
func dir() string { return absOrSelf(config.GetBinFolderPath() + "/tproxy") }

// tproxyServerBinaryPath is the relay binary release.yml builds for every
// platform, though it only ever runs on the one checkPlatform allows.
// Absolute for the same reason as dir(): Start's cmd.Dir is not the panel's
// own working directory, so a relative exec path here resolves against the
// wrong base (see process.go's Start for the doubled-path failure mode this
// produced in practice).
func tproxyServerBinaryPath() string {
	return absOrSelf(config.GetBinFolderPath() + "/tproxy-linux-amd64")
}

// mtproxyBinaryPath is the MTProxy engine binary release.yml builds amd64-only.
func mtproxyBinaryPath() string {
	return absOrSelf(config.GetBinFolderPath() + "/mtproxy-linux-amd64")
}

// absOrSelf resolves p against the process's own working directory,
// falling back to p unchanged on the only realistic failure (os.Getwd
// itself failing) rather than turning a filesystem edge case into a panic.
func absOrSelf(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// IsInstalled reports whether both binaries this package manages are
// present. Neither is downloaded at runtime -- both ship in the release tarball.
func IsInstalled() bool {
	return isRegularFile(tproxyServerBinaryPath()) && isRegularFile(mtproxyBinaryPath())
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func serverConfigPath() string   { return dir() + "/config.json" }
func profilesPath() string       { return dir() + "/profiles.json" }
func tokenKeyPath() string       { return dir() + "/token.key" }
func publicDirPath() string      { return dir() + "/htdocs" }
func proxySecretPath() string    { return dir() + "/proxy-secret" }
func proxyMultiConfPath() string { return dir() + "/proxy-multi.conf" }
