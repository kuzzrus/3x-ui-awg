package hostaction

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileReplacesTheTargetWithTheStagedContentAndTheModeAsked(t *testing.T) {
	useDirect(t)
	path := filepath.Join(t.TempDir(), "warpwp")
	if err := os.WriteFile(path, []byte("older copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := WriteFile(context.Background(), path, []byte("#!/bin/bash\necho new\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := os.ReadFile(path)
	info, statErr := os.Stat(path)
	if err != nil || statErr != nil || string(got) != "#!/bin/bash\necho new\n" || info.Mode().Perm() != 0o755 {
		t.Errorf("the target is %q with mode %v (%v, %v); want the new content, executable", got, info.Mode().Perm(), err, statErr)
	}
	if names := entries(t, filepath.Dir(path)); len(names) != 1 {
		t.Errorf("the folder holds %v, want only the target: no staged copy is left", names)
	}
}

func TestWriteFileCreatesAnEmptyFileWithTheModeAsked(t *testing.T) {
	useDirect(t)
	path := filepath.Join(t.TempDir(), "52-3x-ui")

	if err := WriteFile(context.Background(), path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 || info.Mode().Perm() != 0o600 {
		t.Errorf("the file is %v (%v), want an empty file readable by its owner only", info, err)
	}
}

func TestWriteFileLeavesTheOldBytesToWhoeverHasTheFileOpen(t *testing.T) {
	useDirect(t)
	path := filepath.Join(t.TempDir(), "native.sh")
	if err := os.WriteFile(path, []byte("the copy a scheduled check is executing\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	if err := WriteFile(context.Background(), path, []byte("a new copy\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if still, _ := io.ReadAll(running); string(still) != "the copy a scheduled check is executing\n" {
		t.Errorf("the file that was open now reads %q; it was rewritten in place instead of replaced", still)
	}
}

func TestWriteFileLeavesNothingBesideTheTargetWhenTheReplacementFails(t *testing.T) {
	useDirect(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "native.sh")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	err := WriteFile(context.Background(), target, []byte("x"), 0o755)

	if err == nil {
		t.Fatal("WriteFile onto a directory reported success")
	}
	if names := entries(t, dir); len(names) != 1 || names[0] != "native.sh" {
		t.Errorf("the folder holds %v, want only the directory: the staged copy must go", names)
	}
}

func TestWriteFileAndRemoveNeedAnAbsolutePathBecauseAUnitStartsInTheRootFolder(t *testing.T) {
	useDirect(t)

	if err := WriteFile(context.Background(), "relative/file", []byte("x"), 0o644); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("WriteFile with a relative path = %v, want an error about the absolute path", err)
	}
	if err := Remove(context.Background(), "relative/file"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("Remove with a relative path = %v, want an error about the absolute path", err)
	}
}

func TestRemoveDeletesAFileAndAcceptsOneThatIsNotThere(t *testing.T) {
	useDirect(t)
	path := filepath.Join(t.TempDir(), "52-3x-ui")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Remove(context.Background(), path); err != nil || exists(path) {
		t.Errorf("Remove = %v, file still there: %v; want it gone", err, exists(path))
	}
	if err := Remove(context.Background(), path); err != nil {
		t.Errorf("Remove of a file that is not there = %v, want no error", err)
	}
}
