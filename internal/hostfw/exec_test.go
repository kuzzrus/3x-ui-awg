//go:build !windows

package hostfw

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool writes a command into dir that notes its arguments and the locale it runs in, prints "out", and fails when asked to.
// It uses only shell builtins, so that it runs with a PATH that holds nothing else.
func fakeTool(t *testing.T, dir, name string) (argsFile string) {
	t.Helper()
	argsFile = filepath.Join(dir, name+".args")
	script := "#!/bin/sh\necho \"$@\" > \"$FAKE_ARGS\"\necho \"locale=$LC_ALL/$LANG\"\nif [ -n \"$FAKE_FAIL\" ]; then exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatalf("write the fake %s: %v", name, err)
	}
	t.Setenv("FAKE_ARGS", argsFile)
	t.Setenv("FAKE_FAIL", "")
	return argsFile
}

func TestExecRunnerRunsTheCommandInTheCLocale(t *testing.T) {
	dir := t.TempDir()
	args := fakeTool(t, dir, "nft")
	t.Setenv("PATH", dir)
	t.Setenv("LC_ALL", "ru_RU.UTF-8")
	t.Setenv("LANG", "ru_RU.UTF-8")
	t.Cleanup(SetSearchDirs(nil))

	out, err := ExecRunner(context.Background(), "nft", "-j", "list", "ruleset")
	if err != nil {
		t.Fatalf("ExecRunner: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != "locale=C/C" {
		t.Fatalf("output = %q, want the C locale, which the parsers read", got)
	}
	data, _ := os.ReadFile(args)
	if got := strings.TrimSpace(string(data)); got != "-j list ruleset" {
		t.Fatalf("arguments = %q", got)
	}
}

func TestExecRunnerFindsACommandInTheUsualPlaces(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "iptables")
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetSearchDirs([]string{filepath.Join(dir, "missing"), dir}))

	if _, err := ExecRunner(context.Background(), "iptables", "-S"); err != nil {
		t.Fatalf("a command outside PATH, in a usual place, was not found: %v", err)
	}
}

func TestExecRunnerSaysWhenACommandIsMissingOrFails(t *testing.T) {
	dir := t.TempDir()
	fakeTool(t, dir, "nft")
	t.Setenv("PATH", dir)
	t.Cleanup(SetSearchDirs(nil))

	if _, err := ExecRunner(context.Background(), "ip6tables", "-S"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("a missing command: error = %v, want ErrNotFound", err)
	}
	t.Setenv("FAKE_FAIL", "1")
	if out, err := ExecRunner(context.Background(), "nft", "list"); err == nil {
		t.Fatalf("a command that fails gave %q and no error", out)
	}
}

func TestExecRunnerDoesNotFindADirectoryOrAFileThatCannotRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nft"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "iptables"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	t.Cleanup(SetSearchDirs([]string{dir}))

	for _, name := range []string{"nft", "iptables"} {
		if _, err := ExecRunner(context.Background(), name); !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("%s: error = %v, want ErrNotFound", name, err)
		}
	}
}
