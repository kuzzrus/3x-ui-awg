package tproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped units bound the panel's capabilities, and the engine's privilege drop fails with EPERM
// without these four: a sandboxed panel then neither starts nor stops the engine of a tproxy inbound.
func TestShippedUnitsKeepTheCapabilitiesTheEnginePrivilegeDropNeeds(t *testing.T) {
	for _, name := range []string{"x-ui.service.debian", "x-ui.service.rhel", "x-ui.service.arch"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", name))
			if err != nil {
				t.Fatal(err)
			}
			have := map[string]bool{}
			for _, line := range strings.Split(string(raw), "\n") {
				if list, ok := strings.CutPrefix(strings.TrimSpace(line), "CapabilityBoundingSet="); ok {
					for _, capability := range strings.Fields(list) {
						have[capability] = true
					}
				}
			}
			for _, need := range []string{"CAP_CHOWN", "CAP_SETUID", "CAP_SETGID", "CAP_KILL"} {
				if !have[need] {
					t.Errorf("%s: CapabilityBoundingSet lacks %s", name, need)
				}
			}
		})
	}
}
