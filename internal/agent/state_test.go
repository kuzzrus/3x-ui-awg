package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestGuidIsStable(t *testing.T) {
	dir := t.TempDir()
	state, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.Guid()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(first); err != nil {
		t.Fatalf("guid %q is not a UUID: %v", first, err)
	}
	again, err := state.Guid()
	if err != nil || again != first {
		t.Fatalf("second call = %q, %v; want %q", again, err, first)
	}

	reopened, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Guid(); err != nil || got != first {
		t.Fatalf("after reopening = %q, %v; want %q", got, err, first)
	}
}

func TestGuidIsReplacedWhenTheFileIsDamaged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, guidFile), []byte("not a uuid"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	guid, err := state.Guid()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := uuid.Parse(guid); err != nil {
		t.Fatalf("guid %q is not a UUID: %v", guid, err)
	}
	if again, _ := state.Guid(); again != guid {
		t.Fatalf("the replacement was not stored: %q then %q", guid, again)
	}
}

func TestLastGoodRoundTrip(t *testing.T) {
	state, err := OpenState(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := state.LastGood(); saved != nil || err != nil {
		t.Fatalf("before any save = %v, %v; want nil, nil", saved, err)
	}

	first := []byte("{\n  \"a\": \"<&>\"\n}")
	if err := state.SaveLastGood(first, false); err != nil {
		t.Fatal(err)
	}
	second := []byte("{\"b\":\"\xe2\x80\xa8\"}\n\x00")
	if err := state.SaveLastGood(second, true); err != nil {
		t.Fatal(err)
	}
	saved, err := state.LastGood()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved.Body, second) || !saved.RestartOnUserRemoval {
		t.Fatalf("saved = %q restart=%v, want the second save %q restart=true", saved.Body, saved.RestartOnUserRemoval, second)
	}
}

func TestLastGoodUnreadable(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{"not json", "garbage"},
		{"no config in it", `{"restartOnUserRemoval":true}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, lastGoodFile), []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			state, err := OpenState(dir)
			if err != nil {
				t.Fatal(err)
			}
			saved, err := state.LastGood()
			if saved != nil || err == nil || !strings.Contains(err.Error(), "unreadable") {
				t.Fatalf("LastGood = %v, %v; want an unreadable error", saved, err)
			}
		})
	}
}

func TestStateIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file modes are not enforced on Windows")
	}
	dir := filepath.Join(t.TempDir(), "state")
	state, err := OpenState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.Guid(); err != nil {
		t.Fatal(err)
	}
	if err := state.SaveLastGood([]byte("{}"), false); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("state dir mode = %v, %v; want 0700", info.Mode().Perm(), err)
	}
	for _, name := range []string{guidFile, lastGoodFile} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %v, %v; want 0600", name, info.Mode().Perm(), err)
		}
	}
}
