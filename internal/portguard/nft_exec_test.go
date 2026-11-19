//go:build !windows

package portguard

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeNft writes an nft into dir that notes its arguments and input, and fails with message when message is not empty. It
// uses only shell builtins, so that it runs with a PATH that holds nothing else.
func writeFakeNft(t *testing.T, dir, message string) (argsFile, stdinFile string) {
	t.Helper()
	argsFile, stdinFile = filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	script := "#!/bin/sh\necho \"$@\" > \"$FAKE_NFT_ARGS\"\nwhile IFS= read -r line; do printf '%s\\n' \"$line\"; done > \"$FAKE_NFT_STDIN\"\n" +
		"if [ -n \"$FAKE_NFT_FAIL\" ]; then echo \"$FAKE_NFT_FAIL\" >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the fake nft: %v", err)
	}
	t.Setenv("FAKE_NFT_ARGS", argsFile)
	t.Setenv("FAKE_NFT_STDIN", stdinFile)
	t.Setenv("FAKE_NFT_FAIL", message)
	return argsFile, stdinFile
}

// fakeNft puts an nft in PATH, the only one that can be found: the usual places for it are not searched.
func fakeNft(t *testing.T, message string) (argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, stdinFile = writeFakeNft(t, dir, message)
	t.Setenv("PATH", dir)
	t.Cleanup(SetNftSearchDirs(nil))
	return argsFile, stdinFile
}

// noNft leaves no nft to be found anywhere: PATH is an empty directory, and the usual places for it are not searched.
func noNft(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetNftSearchDirs(nil))
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
	noNft(t)
	err := (NftFirewall{}).Apply(context.Background(), ports(TCP, 8443))
	if err == nil || !strings.Contains(err.Error(), "nft") {
		t.Errorf("Apply error = %v, want it to say nft was not found", err)
	}
	if (NftFirewall{}).Available() {
		t.Error("Available = true without nft in PATH")
	}
}

func TestNftFirewallIsAvailableWhenNftCanListTables(t *testing.T) {
	args, _ := fakeNft(t, "")
	fw := NftFirewall{}
	if !fw.Installed() || !fw.Available() {
		t.Error("Installed or Available is false with a working nft in PATH")
	}
	if got := strings.TrimSpace(readFile(t, args)); got != "list tables" {
		t.Errorf("nft was run with %q, want it asked to list the tables: that is what shows it may read the rules", got)
	}
}

// In a container without NET_ADMIN nft is there and still cannot touch the rules.
func TestNftFirewallIsNotAvailableWhenNftMayNotListTables(t *testing.T) {
	fakeNft(t, "Error: Operation not permitted")
	fw := NftFirewall{}
	if !fw.Installed() {
		t.Error("Installed = false although nft is in PATH")
	}
	if fw.Available() {
		t.Error("Available = true although nft may not list the tables")
	}
}

// A root shell entered with plain su has no /usr/sbin in its PATH on Debian, where nft lives.
func TestNftFirewallFindsNftBeyondPATH(t *testing.T) {
	dir := t.TempDir()
	args, _ := writeFakeNft(t, dir, "")
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetNftSearchDirs([]string{filepath.Join(dir, "missing"), dir}))
	fw := NftFirewall{}
	if !fw.Installed() {
		t.Fatal("Installed = false although nft is in one of the usual places")
	}
	if err := fw.Remove(context.Background()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, args)); got != "delete table inet x_ui_portguard" {
		t.Errorf("nft was run with %q, want it to delete the table", got)
	}
}

func TestNftFirewallIsNotInstalledWhenNftIsNowhere(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetNftSearchDirs([]string{t.TempDir()}))
	fw := NftFirewall{}
	if fw.Installed() || fw.Available() {
		t.Error("Installed or Available is true with no nft anywhere")
	}
}

func TestNftFirewallDoesNotTakeADirectoryOrAFileThatCannotRunForNft(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nft"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "nft"), []byte("not a program"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetNftSearchDirs([]string{dir, other}))
	if (NftFirewall{}).Installed() {
		t.Error("Installed = true for a directory and for a file that is not executable")
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
	noNft(t)
	if err := (NftFirewall{}).Remove(context.Background()); err != nil {
		t.Errorf("Remove error = %v, want none: without nft there are no rules of ours", err)
	}
}
