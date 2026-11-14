package sys

import (
	"net"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestWithoutMultipathTCP(t *testing.T) {
	tests := []struct {
		name, godebug, want string
	}{
		{"unset", "", "multipathtcp=0"},
		{"blank", "  ", "multipathtcp=0"},
		{"other settings are kept", "http2debug=1", "http2debug=1,multipathtcp=0"},
		{"the operator turned it on", "multipathtcp=1", "multipathtcp=1"},
		{"the operator picked a mode among others", "http2debug=1,multipathtcp=3", "http2debug=1,multipathtcp=3"},
		{"already off", "multipathtcp=0", "multipathtcp=0"},
		{"a key that only ends the same is another setting", "xmultipathtcp=1", "xmultipathtcp=1,multipathtcp=0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withoutMultipathTCP(tt.godebug); got != tt.want {
				t.Fatalf("withoutMultipathTCP(%q) = %q, want %q", tt.godebug, got, tt.want)
			}
		})
	}
}

func TestDisableMultipathTCPReachesTheProgramsStarted(t *testing.T) {
	t.Setenv("GODEBUG", "")
	var before net.ListenConfig
	if !before.MultipathTCP() {
		t.Skip("this Go does not listen over MPTCP by default, so there is nothing to switch off")
	}

	DisableMultipathTCP()

	// An exec.Cmd with no Env of its own, which is how every helper program is started, takes this.
	if !slices.Contains(os.Environ(), "GODEBUG=multipathtcp=0") {
		t.Fatalf("GODEBUG is not in the environment the programs started here inherit: %q", os.Getenv("GODEBUG"))
	}
	var lc net.ListenConfig
	if lc.MultipathTCP() {
		t.Fatal("a listener of this process still uses MPTCP")
	}
}

func TestDisableMultipathTCPLeavesTheOperatorsChoice(t *testing.T) {
	t.Setenv("GODEBUG", "multipathtcp=1")
	if line := DisableMultipathTCP(); !strings.Contains(line, "left as it is") {
		t.Fatalf("line = %q", line)
	}
	if got := os.Getenv("GODEBUG"); got != "multipathtcp=1" {
		t.Fatalf("GODEBUG = %q, the operator's setting was changed", got)
	}
}
