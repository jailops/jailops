// Package applylog persists a JSONL transcript of every sync apply and every
// rollout, and provides the fleet lock that serializes them.
//
// Records name what was done (host, kind, name, action, outcome, error text),
// never object contents. Every line also passes through the secrets
// redactor, so a decrypted value cannot reach the file even by accident.
package applylog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"jailops.net/jailops/internal/fleet/secrets"
)

// Event is one JSONL record.
type Event struct {
	Time time.Time      `json:"ts"`
	Kind string         `json:"kind"`
	Data map[string]any `json:"data,omitempty"`
}

// Event kinds.
const (
	KindApplyStart = "apply_start"
	KindChange     = "change"
	KindApplyError = "apply_error"
	KindApplyEnd   = "apply_end"
	KindRollout    = "rollout"
)

// Log is an open apply log. A nil *Log discards everything.
type Log struct {
	mu   sync.Mutex
	f    *os.File
	path string
	red  *secrets.Redactor
	now  func() time.Time
}

// StateDir resolves the state directory: explicit value, $JAILOPS_SYNC_STATE_DIR,
// $XDG_STATE_HOME/jailops-sync, ~/.local/state/jailops-sync.
func StateDir(explicit string) string {
	switch {
	case explicit != "":
		return explicit
	case os.Getenv("JAILOPS_SYNC_STATE_DIR") != "":
		return os.Getenv("JAILOPS_SYNC_STATE_DIR")
	case os.Getenv("XDG_STATE_HOME") != "":
		return filepath.Join(os.Getenv("XDG_STATE_HOME"), "jailops-sync")
	case os.Getenv("HOME") != "":
		return filepath.Join(os.Getenv("HOME"), ".local", "state", "jailops-sync")
	}
	return filepath.Join(os.TempDir(), "jailops-sync")
}

// Open creates apply-<time>-<fleet>.log in dir with mode 0600.
func Open(dir, fleetID string, red *secrets.Redactor) (*Log, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	name := fmt.Sprintf("apply-%s-%s.log", time.Now().UTC().Format("20060102-150405.000000"), fleetID)
	p := filepath.Join(dir, name)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open apply log: %w", err)
	}
	return &Log{f: f, path: p, red: red, now: time.Now}, nil
}

// Path is the log file path ("" for a nil log).
func (l *Log) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

// Write appends one record.
func (l *Log) Write(kind string, data map[string]any) {
	if l == nil || l.f == nil {
		return
	}
	b, err := json.Marshal(Event{Time: l.now().UTC(), Kind: kind, Data: data})
	if err != nil {
		return
	}
	b = append(l.red.Bytes(b), '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.Write(b)
}

// Close closes the file.
func (l *Log) Close() {
	if l != nil && l.f != nil {
		_ = l.f.Close()
	}
}

// Read parses a log file back into events.
func Read(path string) ([]Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Event
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// Lock is the fleet lock: one apply or rollout per fleet at a time on this
// machine. It is an advisory flock on <state>/<fleet>.lock.
type Lock struct{ f *os.File }

// ErrLocked means another sync holds the lock.
var ErrLocked = errors.New("fleet is locked by another jailops-sync")

// Acquire takes the lock, waiting up to wait.
func Acquire(dir, fleetID string, wait time.Duration) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, fleetID+".lock")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock: %w", err)
	}
	deadline := time.Now().Add(wait)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			_ = f.Truncate(0)
			_, _ = f.WriteAt([]byte(fmt.Sprintf("%d\n", os.Getpid())), 0)
			return &Lock{f}, nil
		}
		if time.Now().After(deadline) {
			holder, _ := os.ReadFile(p)
			_ = f.Close()
			return nil, fmt.Errorf("%w (%s, holder pid %q)", ErrLocked, p, string(holder))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Release drops the lock.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}
