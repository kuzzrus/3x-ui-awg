//go:build !windows

package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/hostfw"
)

// The guard's own table drops ports on the input hook, and must not make a host that has no firewall look as if it had one.
func TestHostFirewallLeavesOutTheTableOfThePortGuard(t *testing.T) {
	dir := t.TempDir()
	listing := `{"nftables":[{"table":{"family":"inet","name":"x_ui_portguard"}},{"chain":{"family":"inet","table":"x_ui_portguard","name":"input","type":"filter","hook":"input","prio":-10,"policy":"drop"}}]}`
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte("#!/bin/sh\necho '"+listing+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Cleanup(hostfw.SetSearchDirs(nil))

	got := realDetectHostFirewall(context.Background())
	if !got.Readable || got.Incoming4 != hostfw.PolicyAllow || got.Incoming6 != hostfw.PolicyAllow {
		t.Errorf("a host whose only rules are the guard's = %+v, want it read as one with no firewall", got)
	}
}
