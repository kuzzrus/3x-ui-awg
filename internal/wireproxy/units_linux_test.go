package wireproxy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/hostaction/hostactiontest"
)

// No test can reproduce the sandbox, but its way out can be seen: every action that changes the host starts a unit of
// its own, which is what lets it write under /usr and /etc, and the status poll, which writes nothing, does not.
func TestTheActionsThatChangeTheHostRunAsUnitsAndTheStatusDoesNot(t *testing.T) {
	withEmbeddedStandIns(t)
	managerScript = []byte("#!/usr/bin/env bash\necho \"$*\" >> \"$(dirname \"$0\")/record\"\n[ \"$1\" != --status-json ] || echo '{}'\n")
	fake := hostactiontest.Use(t, "255")
	ctx := context.Background()

	if err := Install(ctx); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := Repair(ctx, RepairCheck); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	if err := FixRouting(ctx); err != nil {
		t.Fatalf("FixRouting: %v", err)
	}
	if err := Uninstall(ctx); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := GetStatus(ctx); err != nil {
		t.Fatalf("GetStatus: %v", err)
	}

	units := fake.Units()
	if len(units) != 6 {
		t.Fatalf("%d units were started, want 6 (two scripts placed, then the install, the check, the routing fix and the removal): %v", len(units), units)
	}
	for i, prefix := range []string{
		"x-ui-hostaction-write-",
		"x-ui-hostaction-write-",
		"x-ui-hostaction-warpwp-install-current-",
		"x-ui-hostaction-warpwp-check-",
		"x-ui-hostaction-warpwp-fix-routing-force-",
		"x-ui-hostaction-warpwp-remove-",
	} {
		if !strings.HasPrefix(units[i], prefix) {
			t.Errorf("unit %d is %q, want one that starts with %q", i, units[i], prefix)
		}
	}
	record := readRecord(t, filepath.Join(filepath.Dir(managerBinPath), "record"))
	if want := "--install-current\n--check\n--fix-routing --force\n--remove\n--status-json\n"; record != want {
		t.Errorf("the manager ran with\n%s\nwant\n%s", record, want)
	}
}
