// Package kv is the deterministic key-value state machine shared by both Raft adapters.
//
// Rules that mirror design section 7 and 11: a command is versioned, carries the proposer's
// generated id, and applying it does no I/O, reads no clock and no randomness. The FSM keeps a
// hash chain over every applied command so two replicas can be compared with one string.
package kv

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"sync"
)

// Version is the only command version this FSM understands.
const Version = 1

// Command is the log payload. Field names are short because tiny commands are benchmarked.
type Command struct {
	V  int    `json:"v"`
	Op string `json:"o"` // set, del, add (integer add)
	K  string `json:"k"`
	X  string `json:"x,omitempty"`
	ID string `json:"i,omitempty"` // proposer-generated id, used by adapters to wake the waiter
}

// Encode marshals a command.
func Encode(c Command) []byte {
	if c.V == 0 {
		c.V = Version
	}
	b, _ := json.Marshal(c)
	return b
}

// FSM is the state machine. OnApply is not state; it only notifies waiters.
type FSM struct {
	mu      sync.Mutex
	data    map[string]string
	count   uint64
	chain   [32]byte
	OnApply func(id string, err error)
}

// New returns an empty FSM.
func New() *FSM { return &FSM{data: map[string]string{}} }

// Apply applies one command. Bad commands are counted and hashed too (deterministically)
// and return an error value; they never panic.
func (f *FSM) Apply(raw []byte) error {
	var c Command
	err := json.Unmarshal(raw, &c)
	f.mu.Lock()
	if err == nil {
		err = f.applyLocked(c)
	}
	h := sha256.New()
	h.Write(f.chain[:])
	h.Write(raw)
	copy(f.chain[:], h.Sum(nil))
	f.count++
	cb := f.OnApply
	f.mu.Unlock()
	if cb != nil {
		cb(c.ID, err)
	}
	return err
}

func (f *FSM) applyLocked(c Command) error {
	if c.V != Version {
		return fmt.Errorf("kv: unsupported command version %d", c.V)
	}
	switch c.Op {
	case "set":
		f.data[c.K] = c.X
	case "del":
		delete(f.data, c.K)
	case "add":
		n, err := strconv.ParseInt(c.X, 10, 64)
		if err != nil {
			return fmt.Errorf("kv: add: %w", err)
		}
		cur, _ := strconv.ParseInt(f.data[c.K], 10, 64)
		f.data[c.K] = strconv.FormatInt(cur+n, 10)
	default:
		return fmt.Errorf("kv: unknown op %q", c.Op)
	}
	return nil
}

type snap struct {
	Count uint64            `json:"count"`
	Chain string            `json:"chain"`
	Data  map[string]string `json:"data"`
}

// Snapshot serialises the state. encoding/json sorts map keys, so it is deterministic.
func (f *FSM) Snapshot() ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(snap{Count: f.count, Chain: hex.EncodeToString(f.chain[:]), Data: f.data})
}

// Restore replaces the state.
func (f *FSM) Restore(b []byte) error {
	var s snap
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	ch, err := hex.DecodeString(s.Chain)
	if err != nil || len(ch) != 32 {
		return fmt.Errorf("kv: bad chain in snapshot")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data = s.Data
	if f.data == nil {
		f.data = map[string]string{}
	}
	f.count = s.Count
	copy(f.chain[:], ch)
	return nil
}

// State returns the applied command count and the hash chain head.
func (f *FSM) State() (uint64, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count, hex.EncodeToString(f.chain[:])
}

// Get reads a key.
func (f *FSM) Get(k string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v, ok := f.data[k]
	return v, ok
}

// Keys returns sorted keys (for tests and diagnostics).
func (f *FSM) Keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	ks := make([]string, 0, len(f.data))
	for k := range f.data {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
