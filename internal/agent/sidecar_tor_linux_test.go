//go:build linux

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/tor"
)

// fakeAptGet installs a tor beside itself, but only once the test lets it, or its folder is gone.
const fakeAptGet = `#!/bin/bash
dir=$(dirname "$0")
if [ "$1" = install ]; then
  echo ready > "$dir/ready"
  while [ ! -e "$dir/release" ] && [ -d "$dir" ]; do sleep 0.05; done
  printf '#!/bin/sh\n' > "$dir/tor"; chmod +x "$dir/tor"
fi
`

const fakeSystemctl = `#!/bin/bash
echo "$*" >> "$(dirname "$0")/systemctl.calls"
`

// The package starts a Tor of its own for the system, which nothing here uses: it is quieted once the install is done,
// also when the call that asked for the install stopped waiting for it long before.
func TestTheSystemTorIsQuietedAfterAnInstallTheCallerStoppedWaitingFor(t *testing.T) {
	if tor.IsAvailable() {
		t.Skip("a real tor is on PATH, so there is nothing to install")
	}
	dir := t.TempDir()
	for name, body := range map[string]string{"apt-get": fakeAptGet, "systemctl": fakeSystemctl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XUI_HOSTACTION", "direct")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for range 500 {
			if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()

	err := torProgram{}.Install(ctx)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want the call to give up with the context error", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 500 {
		if raw, _ := os.ReadFile(filepath.Join(dir, "systemctl.calls")); strings.Contains(string(raw), "disable --now tor@default.service") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the system's own Tor was never stopped and disabled after the install ended")
}
