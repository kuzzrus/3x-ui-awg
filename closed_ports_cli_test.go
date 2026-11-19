//go:build !windows

package main

// openClosedPortsSetting is the way back in over SSH when a closed port has cut the admin off from the panel.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

// fakeNftForCLI puts an nft in PATH that notes its arguments, and fails with message when message is not empty.
func fakeNftForCLI(t *testing.T, message string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$@\" > \"" + argsFile + "\"\n"
	if message != "" {
		script += "echo \"" + message + "\" >&2\nexit 1\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "nft"), []byte(script), 0o755); err != nil {
		t.Fatalf("write the fake nft: %v", err)
	}
	t.Setenv("PATH", dir)
	return argsFile
}

func TestOpenClosedPortsForgetsTheStateAndRemovesTheRules(t *testing.T) {
	newTokenCLIEnv(t)
	settings := service.SettingService{}
	if err := settings.SetPortGuardState(`{"closed":[{"port":8443,"proto":"tcp"}]}`); err != nil {
		t.Fatalf("SetPortGuardState: %v", err)
	}
	args := fakeNftForCLI(t, "")

	if err := openClosedPortsSetting(); err != nil {
		t.Fatalf("openClosedPortsSetting: %v", err)
	}

	if state, err := settings.GetPortGuardState(); err != nil || state != "" {
		t.Errorf("the stored state is %q (%v), want it forgotten: a panel that restarts must not close the ports again", state, err)
	}
	data, err := os.ReadFile(args)
	if err != nil {
		t.Fatalf("nft was not run: %v", err)
	}
	if got := strings.TrimSpace(string(data)); got != "delete table inet x_ui_portguard" {
		t.Errorf("nft was run with %q, want it to delete the table of the closed ports", got)
	}
}

func TestOpenClosedPortsForgetsTheStateEvenWhenTheRulesCannotBeRemoved(t *testing.T) {
	newTokenCLIEnv(t)
	settings := service.SettingService{}
	if err := settings.SetPortGuardState(`{"closed":[{"port":8443,"proto":"tcp"}]}`); err != nil {
		t.Fatalf("SetPortGuardState: %v", err)
	}
	fakeNftForCLI(t, "Error: Operation not permitted")

	if err := openClosedPortsSetting(); err == nil {
		t.Error("openClosedPortsSetting reported nothing wrong although the rules stay")
	}
	if state, _ := settings.GetPortGuardState(); state != "" {
		t.Errorf("the stored state is %q, want it forgotten even then", state)
	}
}
