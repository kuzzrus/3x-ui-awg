// Package hostactiontest stands in for systemd where a test has to see that a change to the host runs as a unit.
package hostactiontest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// systemdRun starts the command the way PID 1 does: detached, with an environment of its own.
const systemdRun = `#!/bin/bash
if [ "$1" = "--version" ]; then echo "systemd $FAKE_SYSTEMD_VERSION ($FAKE_SYSTEMD_VERSION.1-fake)"; exit 0; fi
echo called >> "$FAKE_SYSTEMD_DIR/systemd-run.calls"
if [ -n "$FAKE_SYSTEMD_REFUSE" ]; then echo "$FAKE_SYSTEMD_REFUSE" >&2; exit 1; fi
printf '%s\n' "$@" > "$FAKE_SYSTEMD_DIR/systemd-run.args"
unit=
while [ $# -gt 0 ]; do
  case "$1" in
    --unit) unit=$2; shift 2 ;;
    --*) shift ;;
    *) break ;;
  esac
done
echo "$unit" >> "$FAKE_SYSTEMD_DIR/systemd-run.units"
if [ -n "$FAKE_SYSTEMD_NO_START" ]; then exit 0; fi
setsid env -i PATH="$FAKE_SYSTEMD_UNIT_PATH" "$@" </dev/null >/dev/null 2>&1 &
echo $! > "$FAKE_SYSTEMD_DIR/$unit.pid"
exit 0
`

const systemctl = `#!/bin/bash
echo "$*" >> "$FAKE_SYSTEMD_DIR/systemctl.calls"
case "$1" in
  is-active)
    pid=$(cat "$FAKE_SYSTEMD_DIR/$3.pid" 2>/dev/null) || exit 4
    kill -0 "$pid" 2>/dev/null && exit 0
    exit 3 ;;
  stop)
    pid=$(cat "$FAKE_SYSTEMD_DIR/$2.pid" 2>/dev/null) || exit 5
    kill -TERM -- "-$pid" 2>/dev/null
    for i in $(seq 1 100); do kill -0 "$pid" 2>/dev/null || exit 0; sleep 0.05; done
    kill -KILL -- "-$pid" 2>/dev/null
    exit 0 ;;
esac
exit 0
`

// Fake is the stand-in systemd-run and systemctl, and the files they leave for a test to read.
type Fake struct{ dir string }

// Use puts the stand-ins first on PATH and makes hostaction start commands as units. A unit runs detached, with the PATH
// the test has now and no other setting of the test's own, so a command that needs one has to be handed it.
func Use(t *testing.T, version string) Fake {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{"systemd-run": systemdRun, "systemctl": systemctl} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	unitPath := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+unitPath)
	t.Setenv("FAKE_SYSTEMD_DIR", dir)
	t.Setenv("FAKE_SYSTEMD_VERSION", version)
	t.Setenv("FAKE_SYSTEMD_UNIT_PATH", unitPath)
	t.Setenv("FAKE_SYSTEMD_REFUSE", "")
	t.Setenv("FAKE_SYSTEMD_NO_START", "")
	t.Setenv("XUI_HOSTACTION", "unit")
	t.Setenv("XUI_BIN_FOLDER", t.TempDir())
	return Fake{dir: dir}
}

// Read is the content of a file the stand-ins left, such as systemd-run.args or systemctl.calls.
func (f Fake) Read(name string) string {
	raw, _ := os.ReadFile(filepath.Join(f.dir, name))
	return string(raw)
}

// Args are the arguments of the last systemd-run call that was not refused.
func (f Fake) Args() []string {
	return strings.Split(strings.TrimSpace(f.Read("systemd-run.args")), "\n")
}

// Unit is the unit the last systemd-run call that was not refused started.
func (f Fake) Unit() string {
	args := f.Args()
	for i, arg := range args {
		if arg == "--unit" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// Units are all the units started so far, in order.
func (f Fake) Units() []string {
	text := strings.TrimSpace(f.Read("systemd-run.units"))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}
