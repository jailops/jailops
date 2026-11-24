package plan

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Checks remembers, in the state directory of sync, when a dry run last found a recipe of a jail converged. It is only
// a memory of what sync saw: the truth is the jail. Losing the file makes the next run look again.
type Checks struct {
	path string

	mu    sync.Mutex
	m     map[string]Check
	dirty bool
}

// Check is the last dry run that found a recipe converged.
type Check struct {
	// Hash is the hash of the recipe the check was made for.
	Hash string    `json:"hash"`
	At   time.Time `json:"at"`
}

// OpenChecks opens the memory of the fleet in dir. A file that cannot be read is an empty memory.
func OpenChecks(dir, fleetID string) *Checks {
	c := &Checks{path: filepath.Join(dir, "recipe-checks-"+fleetID+".json"), m: map[string]Check{}}
	if b, err := os.ReadFile(c.path); err == nil {
		_ = json.Unmarshal(b, &c.m)
		if c.m == nil {
			c.m = map[string]Check{}
		}
	}
	return c
}

// Converged returns the last converged check of key.
func (c *Checks) Converged(key string) (Check, bool) {
	if c == nil {
		return Check{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ck, ok := c.m[key]
	return ck, ok
}

// Set records a converged check; Forget removes the record.
func (c *Checks) Set(key, hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = Check{Hash: hash, At: time.Now()}
	c.dirty = true
}

// Forget drops the record of key.
func (c *Checks) Forget(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok {
		delete(c.m, key)
		c.dirty = true
	}
}

// Save writes the memory when it changed (temporary file, rename). An error is returned for the log; it never fails a run.
func (c *Checks) Save() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.dirty {
		return nil
	}
	b, err := json.Marshal(c.m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".recipe-checks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), c.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	c.dirty = false
	return nil
}
