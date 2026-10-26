package datastore

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// Mode is the maintenance mode of a repository.
type Mode int32

// Modes. Online is normal operation. ReadOnly lets restores, browsing and
// verification reads continue while every write (new backups, retention,
// garbage collection, quarantine) is refused, so a storage migration or a
// snapshot of the datastore sees a quiet repository. Offline refuses every
// access: it is for taking the storage away.
const (
	ModeOnline Mode = iota
	ModeReadOnly
	ModeOffline
)

// Maintenance-mode errors. Hosts see them as "temporarily unavailable" and
// retry later.
var (
	ErrReadOnly = errors.New("repository is in read-only maintenance mode")
	ErrOffline  = errors.New("repository is offline for maintenance")
)

// String is the name used in the API.
func (m Mode) String() string {
	switch m {
	case ModeReadOnly:
		return "read-only"
	case ModeOffline:
		return "offline"
	}
	return "online"
}

// ParseMode converts the API name.
func ParseMode(s string) (Mode, bool) {
	switch s {
	case "online", "":
		return ModeOnline, true
	case "read-only":
		return ModeReadOnly, true
	case "offline":
		return ModeOffline, true
	}
	return ModeOnline, false
}

// ModeBackend wraps a Backend and enforces a maintenance mode that can change
// while the repository is in use. It sits below the whole object model, so no
// code path can write around it.
type ModeBackend struct {
	inner Backend
	mode  atomic.Int32
}

// NewModeBackend wraps b in the given mode.
func NewModeBackend(b Backend, m Mode) *ModeBackend {
	mb := &ModeBackend{inner: b}
	mb.mode.Store(int32(m))
	return mb
}

// SetMode changes the mode; it takes effect for the next backend call.
func (b *ModeBackend) SetMode(m Mode) { b.mode.Store(int32(m)) }

// Mode returns the current mode.
func (b *ModeBackend) Mode() Mode { return Mode(b.mode.Load()) }

// Unwrap returns the wrapped backend.
func (b *ModeBackend) Unwrap() Backend { return b.inner }

func (b *ModeBackend) read() error {
	if b.Mode() == ModeOffline {
		return ErrOffline
	}
	return nil
}

func (b *ModeBackend) write() error {
	switch b.Mode() {
	case ModeOffline:
		return ErrOffline
	case ModeReadOnly:
		return ErrReadOnly
	}
	return nil
}

// Kind implements Backend.
func (b *ModeBackend) Kind() string { return b.inner.Kind() }

// Put implements Backend.
func (b *ModeBackend) Put(ctx context.Context, key string, r io.Reader, size int64, o PutOptions) error {
	if err := b.write(); err != nil {
		return err
	}
	return b.inner.Put(ctx, key, r, size, o)
}

// Get implements Backend.
func (b *ModeBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := b.read(); err != nil {
		return nil, err
	}
	return b.inner.Get(ctx, key)
}

// GetRange implements Backend.
func (b *ModeBackend) GetRange(ctx context.Context, key string, off, n int64) (io.ReadCloser, error) {
	if err := b.read(); err != nil {
		return nil, err
	}
	return b.inner.GetRange(ctx, key, off, n)
}

// Head implements Backend.
func (b *ModeBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := b.read(); err != nil {
		return ObjectInfo{}, err
	}
	return b.inner.Head(ctx, key)
}

// List implements Backend.
func (b *ModeBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	if err := b.read(); err != nil {
		return err
	}
	return b.inner.List(ctx, prefix, fn)
}

// Delete implements Backend.
func (b *ModeBackend) Delete(ctx context.Context, key string) error {
	if err := b.write(); err != nil {
		return err
	}
	return b.inner.Delete(ctx, key)
}

// Unwrap peels wrappers (maintenance mode, fault injection) off a backend to
// reach the storage underneath.
func Unwrap(b Backend) Backend {
	for {
		u, ok := b.(interface{ Unwrap() Backend })
		if !ok {
			return b
		}
		b = u.Unwrap()
	}
}
