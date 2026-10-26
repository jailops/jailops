package datastore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MemBackend is an in-memory Backend with strict object-store semantics: no
// directories, no rename, whole-object puts, atomic create-only writes. The
// repository's tests run against it and against the directory backend, which
// keeps the repository free of POSIX assumptions. It is not for production.
type MemBackend struct {
	mu   sync.Mutex
	objs map[string]memObj
	now  func() time.Time

	// Fault, when set, is called before every operation with the operation
	// ("put", "get", "head", "list", "delete") and the key (the prefix for
	// "list"); a non-nil error fails the operation and changes nothing.
	Fault func(op, key string) error
	// TornPut, when set, is called for every Put after the object has been
	// read; true makes the Put fail with ErrTorn and store nothing, as an
	// upload that broke off in the middle does.
	TornPut func(key string) bool

	// Counters.
	Puts, Gets, Heads, Lists, Deletes atomic.Int64
	// BytesRead counts the bytes handed out by Get and GetRange.
	BytesRead atomic.Int64
}

// ErrTorn is returned by a Put that the TornPut hook interrupted.
var ErrTorn = errors.New("datastore: put interrupted")

type memObj struct {
	data []byte
	mod  time.Time
}

// NewMemBackend returns an empty backend.
func NewMemBackend() *MemBackend {
	return &MemBackend{objs: map[string]memObj{}, now: time.Now}
}

// Kind implements Backend.
func (m *MemBackend) Kind() string { return "memory" }

// SetClock replaces the clock (tests).
func (m *MemBackend) SetClock(f func() time.Time) {
	m.mu.Lock()
	m.now = f
	m.mu.Unlock()
}

func (m *MemBackend) fault(op, key string) error {
	if m.Fault != nil {
		return m.Fault(op, key)
	}
	return nil
}

// Put implements Backend.
func (m *MemBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.Puts.Add(1)
	if err := m.fault("put", key); err != nil {
		return err
	}
	data, err := readExactly(&ctxReader{ctx, r}, size)
	if err != nil {
		return err
	}
	if m.TornPut != nil && m.TornPut(key) {
		return ErrTorn
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.objs[key]; ok && opts.IfAbsent {
		return ErrExists
	}
	m.objs[key] = memObj{data: data, mod: m.now()}
	return nil
}

// Get implements Backend.
func (m *MemBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return m.GetRange(ctx, key, 0, -1)
}

// GetRange implements Backend. A length that runs past the end is cut at the
// end; an offset at the end gives an empty read; an offset beyond the end is an
// error.
func (m *MemBackend) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	if err := ValidKey(key); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.Gets.Add(1)
	if err := m.fault("get", key); err != nil {
		return nil, err
	}
	m.mu.Lock()
	o, ok := m.objs[key]
	m.mu.Unlock()
	if !ok {
		return nil, ErrNotFound
	}
	lo, hi, err := clampRange(int64(len(o.data)), offset, length)
	if err != nil {
		return nil, err
	}
	m.BytesRead.Add(hi - lo)
	// objects are immutable once stored (replaced, never edited), so the slice can be shared
	return io.NopCloser(bytes.NewReader(o.data[lo:hi])), nil
}

// ErrRange is returned for a range read that starts beyond the object.
var ErrRange = errors.New("datastore: range not satisfiable")

func clampRange(size, offset, length int64) (lo, hi int64, err error) {
	if offset < 0 || offset > size {
		return 0, 0, ErrRange
	}
	hi = size
	if length >= 0 && offset+length < size {
		hi = offset + length
	}
	return offset, hi, nil
}

// Head implements Backend.
func (m *MemBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	if err := ValidKey(key); err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	m.Heads.Add(1)
	if err := m.fault("head", key); err != nil {
		return ObjectInfo{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.objs[key]
	if !ok {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{Key: key, Size: int64(len(o.data)), ModTime: o.mod}, nil
}

// List implements Backend.
func (m *MemBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	if err := validPrefix(prefix); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.Lists.Add(1)
	if err := m.fault("list", prefix); err != nil {
		return err
	}
	m.mu.Lock()
	infos := make([]ObjectInfo, 0, 64)
	for k, o := range m.objs {
		if strings.HasPrefix(k, prefix) {
			infos = append(infos, ObjectInfo{Key: k, Size: int64(len(o.data)), ModTime: o.mod})
		}
	}
	m.mu.Unlock()
	sort.Slice(infos, func(i, j int) bool { return infos[i].Key < infos[j].Key })
	for _, in := range infos {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(in); err != nil {
			return err
		}
	}
	return nil
}

// Delete implements Backend.
func (m *MemBackend) Delete(ctx context.Context, key string) error {
	if err := ValidKey(key); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	m.Deletes.Add(1)
	if err := m.fault("delete", key); err != nil {
		return err
	}
	m.mu.Lock()
	delete(m.objs, key)
	m.mu.Unlock()
	return nil
}

// ---- test helpers -----------------------------------------------------------------

// SetModTime backdates an object.
func (m *MemBackend) SetModTime(key string, t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.objs[key]; ok {
		o.mod = t
		m.objs[key] = o
	}
}

// Corrupt flips a byte of an object in place (bit rot).
func (m *MemBackend) Corrupt(key string, at int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.objs[key]; ok && at < len(o.data) {
		o.data = append([]byte(nil), o.data...)
		o.data[at] ^= 0xff
		m.objs[key] = o
	}
}

// Overwrite replaces an object bypassing create-only semantics (an attacker
// or a buggy store writing around the repository).
func (m *MemBackend) Overwrite(key string, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[key] = memObj{data: append([]byte(nil), data...), mod: m.now()}
}

// Keys returns every key, sorted.
func (m *MemBackend) Keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objs))
	for k := range m.objs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ctxReader fails reads once the context is done.
type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
