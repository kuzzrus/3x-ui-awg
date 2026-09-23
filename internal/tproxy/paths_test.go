package tproxy

import (
	"path/filepath"
	"testing"
)

// Regression: with XUI_BIN_FOLDER unset (every real deployment -- it exists
// only for tests), config.GetBinFolderPath() returns the relative literal
// "bin". dir() and the binary-path helpers used to return that relative
// value verbatim; Start sets a child's cmd.Dir to dir(), so a relative
// value there is what any relative argument (including the child's own
// exec path) then resolves against -- not the panel's own cwd. The result
// was every one of these paths pointing one directory too deep from the
// child's perspective (e.g. bin/tproxy/bin/tproxy/proxy-multi.conf), so the
// real binary and its own config file were both never found, even though
// both objectively existed exactly where release.yml and this package's
// own file-writing code put them.
func TestPathHelpersAreAbsoluteWithDefaultBinFolder(t *testing.T) {
	t.Setenv("XUI_BIN_FOLDER", "")

	for name, got := range map[string]string{
		"dir":                    dir(),
		"tproxyServerBinaryPath": tproxyServerBinaryPath(),
		"mtproxyBinaryPath":      mtproxyBinaryPath(),
		"serverConfigPath":       serverConfigPath(),
		"profilesPath":           profilesPath(),
		"tokenKeyPath":           tokenKeyPath(),
		"publicDirPath":          publicDirPath(),
		"proxySecretPath":        proxySecretPath(),
		"proxyMultiConfPath":     proxyMultiConfPath(),
	} {
		if !filepath.IsAbs(got) {
			t.Errorf("%s() = %q, want an absolute path", name, got)
		}
	}
}

func TestCheckPlatform(t *testing.T) {
	if err := checkPlatform("linux", "amd64"); err != nil {
		t.Errorf("checkPlatform(linux, amd64) = %v, want nil", err)
	}
	for _, c := range [][2]string{{"linux", "arm64"}, {"windows", "amd64"}, {"darwin", "amd64"}} {
		if err := checkPlatform(c[0], c[1]); err == nil {
			t.Errorf("checkPlatform(%s, %s) = nil, want an error", c[0], c[1])
		}
	}
}
