//go:build !windows

package portguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeNft puts an nft in PATH that notes its arguments and input, and fails with message when message is not empty.
func fakeNft(t *testing.T, message string) (argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, stdinFile = filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	// Only shell builtins: PATH holds nothing but this directory.
	script := "#!/bin/sh\necho \"$@\" > \"$FAKE_NFT_ARGS\"\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done > \"$FAKE_NFT_STDIN\"\n" +
		"if [ -n \"$FAKE_NFT_FAIL\" ]; then echo \"$FAKE_NFT_FAIL\" >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the fake nft: %v", err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_NFT_ARGS", argsFile)
	t.Setenv("FAKE_NFT_STDIN", stdinFile)
	t.Setenv("FAKE_NFT_FAIL", message)
	return argsFile, stdinFile
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestNftFirewallAppliesTheRulesetThroughNft(t *testing.T) {
	args, stdin := fakeNft(t, "")
	ports := []Port{{8443, TCP}, {51820, UDP}}
	if err := (NftFirewall{}).Apply(context.Background(), ports); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, args)); got != "-f -" {
		t.Errorf("nft was run with %q, want \"-f -\"", got)
	}
	if got, want := readFile(t, stdin), renderRuleset(ports); got != want {
		t.Errorf("nft read\n%s\nwant\n%s", got, want)
	}
}

func TestNftFirewallApplyReportsWhatNftSaid(t *testing.T) {
	fakeNft(t, "Error: syntax error")
	err := (NftFirewall{}).Apply(context.Background(), ports(TCP, 8443))
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Errorf("Apply error = %v, want it to hold what nft said", err)
	}
}

func TestNftFirewallApplyWithoutNftSaysSo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := (NftFirewall{}).Apply(context.Background(), ports(TCP, 8443))
	if err == nil || !strings.Contains(err.Error(), "nft") {
		t.Errorf("Apply error = %v, want it to say nft was not found", err)
	}
	if (NftFirewall{}).Available() {
		t.Error("Available = true without nft in PATH")
	}
}

func TestNftFirewallIsAvailableWithNft(t *testing.T) {
	fakeNft(t, "")
	if !(NftFirewall{}).Available() {
		t.Error("Available = false with nft in PATH")
	}
}

func TestNftFirewallRemoveDeletesTheTable(t *testing.T) {
	args, _ := fakeNft(t, "")
	if err := (NftFirewall{}).Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, args)); got != "delete table inet x_ui_portguard" {
		t.Errorf("nft was run with %q, want it to delete the table", got)
	}
}

func TestNftFirewallRemoveOfAnAbsentTableIsNoError(t *testing.T) {
	fakeNft(t, "Error: No such file or directory; did you mean table 'x' in family inet?")
	if err := (NftFirewall{}).Remove(context.Background()); err != nil {
		t.Errorf("Remove error = %v, want none: a host that never closed a port has no table", err)
	}
}

func TestNftFirewallRemoveReportsAnyOtherFailure(t *testing.T) {
	fakeNft(t, "Error: Operation not permitted")
	err := (NftFirewall{}).Remove(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Errorf("Remove error = %v, want it to hold what nft said", err)
	}
}

func TestNftFirewallRemoveWithoutNftHasNothingToRemove(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := (NftFirewall{}).Remove(context.Background()); err != nil {
		t.Errorf("Remove error = %v, want none: without nft there are no rules of ours", err)
	}
}
