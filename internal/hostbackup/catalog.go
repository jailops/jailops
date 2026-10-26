package hostbackup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"jailops.net/jailops/sdk/api"
)

// maxRuns bounds the run history a catalog keeps.
const maxRuns = 5000

// MemCatalog is a Catalog in memory. With a path it also persists itself
// (temp file, fsync, rename), so a daemon restart keeps its history.
type MemCatalog struct {
	mu      sync.Mutex
	path    string
	backups map[string]api.Backup
	runs    []Run
}

type catalogFile struct {
	Version int          `json:"version"`
	Backups []api.Backup `json:"backups"`
	Runs    []Run        `json:"runs"`
}

// NewMemCatalog returns an empty in-memory catalog.
func NewMemCatalog() *MemCatalog { return &MemCatalog{backups: map[string]api.Backup{}} }

// OpenFileCatalog loads (or starts) a catalog persisted at path.
func OpenFileCatalog(path string) (*MemCatalog, error) {
	c := NewMemCatalog()
	c.path = path
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	var f catalogFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, api.Errorf(api.CodeInternal, "backup catalog %s is damaged: %v", path, err).
			WithHint("delete it; it is rebuilt from the repository by a reindex")
	}
	for _, bk := range f.Backups {
		c.backups[bk.Metadata.Name] = bk
	}
	c.runs = f.Runs
	return c, nil
}

func (c *MemCatalog) saveLocked() error {
	if c.path == "" {
		return nil
	}
	f := catalogFile{Version: 1, Runs: c.runs}
	for _, b := range c.backups {
		f.Backups = append(f.Backups, b)
	}
	sort.Slice(f.Backups, func(i, j int) bool { return f.Backups[i].Metadata.Name < f.Backups[j].Metadata.Name })
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(c.path), ".catalog-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, c.path); err != nil {
		os.Remove(name)
		return err
	}
	if d, err := os.Open(filepath.Dir(c.path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// PutBackup implements Catalog.
func (c *MemCatalog) PutBackup(_ context.Context, b api.Backup) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.backups[b.Metadata.Name] = b
	return c.saveLocked()
}

// Backups implements Catalog: newest first.
func (c *MemCatalog) Backups(context.Context) ([]api.Backup, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]api.Backup, 0, len(c.backups))
	for _, b := range c.backups {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Name > out[j].Metadata.Name })
	return out, nil
}

// GetBackup implements Catalog.
func (c *MemCatalog) GetBackup(_ context.Context, id string) (api.Backup, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.backups[id]
	return b, ok, nil
}

// DeleteBackup implements Catalog.
func (c *MemCatalog) DeleteBackup(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.backups, id)
	return c.saveLocked()
}

// PutRun implements Catalog.
func (c *MemCatalog) PutRun(_ context.Context, r Run) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.runs {
		if c.runs[i].ID == r.ID {
			c.runs[i] = r
			return c.saveLocked()
		}
	}
	c.runs = append(c.runs, r)
	if len(c.runs) > maxRuns {
		c.runs = append([]Run(nil), c.runs[len(c.runs)-maxRuns:]...)
	}
	return c.saveLocked()
}

// Runs implements Catalog: oldest first.
func (c *MemCatalog) Runs(context.Context) ([]Run, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Run(nil), c.runs...), nil
}
