package hostfw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// commandTimeout bounds every command, so that a firewall tool that hangs cannot hold up the page that asks.
const commandTimeout = 5 * time.Second

// maxOutput is the most a tool may print: the ruleset of a host that runs many containers is a few megabytes.
const maxOutput = 16 << 20

var errTooLarge = errors.New("the output is too large")

// searchDirs are tried after PATH: a root shell entered with plain su has no /usr/sbin in its PATH on Debian.
var searchDirs = []string{"/usr/sbin", "/sbin", "/usr/bin", "/bin"}

// SetSearchDirs replaces the directories searched after PATH, so that a test does not find the tools of the host it runs on;
// the returned func puts them back.
func SetSearchDirs(dirs []string) (restore func()) {
	before := searchDirs
	searchDirs = dirs
	return func() { searchDirs = before }
}

// lookup is the path of a command, from PATH or else from the usual places for it.
func lookup(name string) (string, error) {
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	for _, dir := range searchDirs {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", exec.ErrNotFound
}

type boundedBuffer struct{ bytes.Buffer }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > maxOutput {
		return 0, errTooLarge
	}
	return b.Buffer.Write(p)
}

// ExecRunner is the Runner of the host. The command runs in the C locale, so that its answer has the words that are looked for.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	path, err := lookup(name)
	if err != nil {
		return nil, fmt.Errorf("hostfw: %s was not found: %w", name, err)
	}
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	var out boundedBuffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("hostfw: %s: %w", name, err)
	}
	return out.Bytes(), nil
}
