package datastore

import (
	"context"
	"errors"
	"io"
	"sync"
)

// ErrCrash is returned by a FaultBackend once its crash point is reached.
var ErrCrash = errors.New("datastore: injected crash")

// FaultBackend wraps a Backend and "crashes" at a chosen point: the Nth
// mutating call, or the first mutating call on a key that matches, fails
// before it reaches the storage, and every mutating call after it fails too, as
// if the process had died. Reads keep working so a test can look at what the
// crash left behind. It is for tests of crash safety.
type FaultBackend struct {
	inner Backend

	mu      sync.Mutex
	calls   int
	crashAt int // 0 = never; crash on the crashAt-th mutating call (1-based)
	match   func(op, key string) bool
	crashed bool
	// Log records "put key" and "delete key" for every mutating call that was let through.
	Log []string
}

// NewFaultBackend wraps b.
func NewFaultBackend(b Backend) *FaultBackend { return &FaultBackend{inner: b} }

// CrashAtCall makes the nth (1-based) mutating call, and all later ones, fail.
func (f *FaultBackend) CrashAtCall(n int) {
	f.mu.Lock()
	f.crashAt, f.calls, f.crashed, f.match = n, 0, false, nil
	f.mu.Unlock()
}

// CrashWhen makes the first mutating call for which match returns true, and
// all later ones, fail.
func (f *FaultBackend) CrashWhen(match func(op, key string) bool) {
	f.mu.Lock()
	f.match, f.crashAt, f.crashed = match, 0, false
	f.mu.Unlock()
}

// Reset ends a crash: the "restarted process" uses the storage again.
func (f *FaultBackend) Reset() {
	f.mu.Lock()
	f.crashAt, f.match, f.crashed, f.calls = 0, nil, false, 0
	f.mu.Unlock()
}

// Crashed reports whether the crash point was reached.
func (f *FaultBackend) Crashed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.crashed
}

// Calls returns the number of mutating calls let through or refused since CrashAtCall/Reset.
func (f *FaultBackend) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Unwrap returns the wrapped backend.
func (f *FaultBackend) Unwrap() Backend { return f.inner }

func (f *FaultBackend) gate(op, key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if !f.crashed {
		if (f.crashAt > 0 && f.calls >= f.crashAt) || (f.match != nil && f.match(op, key)) {
			f.crashed = true
		}
	}
	if f.crashed {
		return ErrCrash
	}
	f.Log = append(f.Log, op+" "+key)
	return nil
}

// Kind implements Backend.
func (f *FaultBackend) Kind() string { return f.inner.Kind() }

// Put implements Backend.
func (f *FaultBackend) Put(ctx context.Context, key string, r io.Reader, size int64, o PutOptions) error {
	if err := f.gate("put", key); err != nil {
		return err
	}
	return f.inner.Put(ctx, key, r, size, o)
}

// Delete implements Backend.
func (f *FaultBackend) Delete(ctx context.Context, key string) error {
	if err := f.gate("delete", key); err != nil {
		return err
	}
	return f.inner.Delete(ctx, key)
}

// Get implements Backend.
func (f *FaultBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return f.inner.Get(ctx, key)
}

// GetRange implements Backend.
func (f *FaultBackend) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	return f.inner.GetRange(ctx, key, off, n)
}

// Head implements Backend.
func (f *FaultBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	return f.inner.Head(ctx, key)
}

// List implements Backend.
func (f *FaultBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	return f.inner.List(ctx, prefix, fn)
}
