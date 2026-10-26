package rollout

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Rollout and item states.
const (
	StatusRunning = "running"
	StatusFailed  = "failed"
	StatusAborted = "aborted"
	StatusDone    = "done"

	ItemPending   = "pending"
	ItemUpgrading = "upgrading"
	ItemVerifying = "verifying"
	ItemDone      = "done"
	ItemFailed    = "failed"
)

// Item is one jail of a rollout.
type Item struct {
	Host  string `json:"host"`
	Jail  string `json:"jail"`
	From  string `json:"from"`
	To    string `json:"to"`
	State string `json:"state"`
	// Op is the id of the upgrade operation on the host while it runs, so a
	// resumed rollout waits for it instead of starting a second upgrade.
	Op    string `json:"op,omitempty"`
	Error string `json:"error,omitempty"`
	Note  string `json:"note,omitempty"`
}

// State is the persisted progress of one rollout. It is written after every
// transition (temp file, rename), which is what makes a rollout resumable
// after a crash, a lost connection or a failed health gate.
type State struct {
	Name     string    `json:"name"`
	Fleet    string    `json:"fleet"`
	Project  string    `json:"project,omitempty"`
	SpecHash string    `json:"spec_hash"`
	Status   string    `json:"status"`
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
	Items    []*Item   `json:"items"`
	// Batches are index ranges into Items: canary first, then batch_size each.
	Batches [][2]int `json:"batches"`
}

// Counts returns items per state.
func (s *State) Counts() map[string]int {
	m := map[string]int{}
	for _, it := range s.Items {
		m[it.State]++
	}
	return m
}

// Store persists rollout states under one directory.
type Store struct{ Dir string }

func (s Store) path(name string) string { return filepath.Join(s.Dir, name+".json") }

// ErrNoState means the rollout has never run.
var ErrNoState = errors.New("rollout has no recorded state")

// Load reads a rollout state.
func (s Store) Load(name string) (*State, error) {
	b, err := os.ReadFile(s.path(name))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrNoState
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("rollout state %s: %w", s.path(name), err)
	}
	return &st, nil
}

// Save writes the state atomically.
func (s Store) Save(st *State) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.Dir, ".rollout-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(st.Name))
}

// Remove deletes a recorded state.
func (s Store) Remove(name string) error {
	err := os.Remove(s.path(name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
