package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

const (
	guidFile     = "guid"
	lastGoodFile = "last-good.json"
	stateDirMode = 0o700
	stateMode    = 0o600
)

// State is the agent's small memory on disk: its identity and the config that last ran.
type State struct {
	dir string
	mu  sync.Mutex // makes the first Guid read-or-create exclusive
}

// OpenState creates the folder if needed; everything in it is private to the agent.
func OpenState(dir string) (*State, error) {
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return nil, err
	}
	return &State{dir: dir}, nil
}

// Guid is the agent's stable identity, created on first use. The master learns it
// from every status and uses it to tell nodes apart across address changes.
func (s *State) Guid() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.dir, guidFile)
	raw, err := os.ReadFile(path)
	if err == nil {
		if guid, parseErr := uuid.Parse(strings.TrimSpace(string(raw))); parseErr == nil {
			return guid.String(), nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	guid := uuid.NewString()
	if err := xray.WriteFileAtomic(path, []byte(guid+"\n"), stateMode); err != nil {
		return "", err
	}
	return guid, nil
}

// Saved is a config that ran: the exact body the master sent and the policy it came
// with, from which the revision is recomputed.
type Saved struct {
	Body                 []byte `json:"config"`
	RestartOnUserRemoval bool   `json:"restartOnUserRemoval"`
}

// LastGood returns the last config that started, or nil before the first one.
func (s *State) LastGood() (*Saved, error) {
	raw, err := os.ReadFile(filepath.Join(s.dir, lastGoodFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var saved Saved
	if err := json.Unmarshal(raw, &saved); err != nil || len(saved.Body) == 0 {
		return nil, fmt.Errorf("last good config in %s is unreadable", s.dir)
	}
	return &saved, nil
}

// SaveLastGood replaces the stored config in one atomic step, so a crash leaves
// either the old one or the new one and never a torn file.
func (s *State) SaveLastGood(body []byte, restartOnUserRemoval bool) error {
	raw, err := json.Marshal(Saved{Body: body, RestartOnUserRemoval: restartOnUserRemoval})
	if err != nil {
		return err
	}
	return xray.WriteFileAtomic(filepath.Join(s.dir, lastGoodFile), raw, stateMode)
}
