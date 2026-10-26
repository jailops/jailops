// Package datastore defines the object storage interface under the backup
// repository and its implementations (memory, directory, S3).
//
// The repository logic above this interface makes no POSIX assumptions: keys
// are flat strings with '/' separators, objects are immutable once written
// (writes are create-only), and listing is by prefix.
package datastore

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrNotFound is returned when an object does not exist.
var ErrNotFound = errors.New("datastore: object not found")

// ErrExists is returned by a create-only Put when the object already exists.
var ErrExists = errors.New("datastore: object already exists")

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time // object write time; garbage collection ages objects by this
}

// PutOptions control a write.
type PutOptions struct {
	// IfAbsent makes the write create-only and atomic: it succeeds only if the
	// key does not exist, otherwise it returns ErrExists. Every write by the
	// repository sets it, except where a key is explicitly documented as
	// replaceable.
	IfAbsent bool
}

// Backend is an object store.
//
// Implementations must guarantee: a successful Put is durable and visible to
// Head, Get and List; a failed or torn Put leaves no object behind; IfAbsent
// is atomic under concurrent writers; Delete of a missing key is not an error;
// List returns every key under the prefix, in lexical order, exactly once.
type Backend interface {
	// Put writes size bytes from r. size must be exact; a short or long
	// reader is an error and leaves nothing behind.
	Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error
	// Get returns the whole object. The caller closes the reader.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// GetRange returns length bytes starting at offset; length < 0 means to the end.
	GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error)
	// Head returns object metadata or ErrNotFound.
	Head(ctx context.Context, key string) (ObjectInfo, error)
	// List calls fn for every object whose key starts with prefix, in lexical
	// order. If fn returns an error the listing stops and the error is returned.
	List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error
	// Delete removes an object. Deleting a missing key succeeds.
	Delete(ctx context.Context, key string) error
	// Kind names the backend: "memory", "directory" or "s3".
	Kind() string
}
