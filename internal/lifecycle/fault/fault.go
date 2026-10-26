// Package fault provides a failure injector shared by the in-memory ZFS model
// and the fakes. Every mutating operation calls Hit; a test sweeps FailAt from
// 1 to the number of operations to fail each step in turn.
package fault

import (
	"errors"
	"fmt"
	"sync"
)

// ErrInjected is the error returned by an injected failure.
var ErrInjected = errors.New("injected failure")

// Injector counts mutating operations and fails the FailAt-th one.
type Injector struct {
	mu     sync.Mutex
	FailAt int // 0 = never fail
	count  int
	Log    []string
}

// Hit records op and returns ErrInjected (wrapped with op) when it is the
// operation selected for failure. A nil Injector never fails.
func (i *Injector) Hit(op string) error {
	if i == nil {
		return nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.count++
	i.Log = append(i.Log, op)
	if i.FailAt != 0 && i.count == i.FailAt {
		return fmt.Errorf("%s: %w", op, ErrInjected)
	}
	return nil
}

// Count returns the number of mutating operations seen.
func (i *Injector) Count() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.count
}

// Reset zeroes the counter and log and sets the failing operation.
func (i *Injector) Reset(failAt int) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.count, i.FailAt, i.Log = 0, failAt, nil
}
