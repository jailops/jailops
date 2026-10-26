// Package imagegen builds and manages image generations: immutable datasets
// built from a base source (pkgbase, Alpine minirootfs, Debian debootstrap),
// each with a @ro snapshot that never changes. A new generation is built in a
// staging clone of its parent generation, validated, snapshotted and renamed
// into place; the rename is the commit point.
package imagegen

import "context"

// ZFS is the narrow part of ZFS this package uses. Get returns the local value
// of a property ("" when not set locally).
type ZFS interface {
	Exists(ctx context.Context, name string) (bool, error)
	Create(ctx context.Context, name string, props map[string]string) error
	Clone(ctx context.Context, snapshot, target string, props map[string]string) error
	Snapshot(ctx context.Context, props map[string]string, snapshots ...string) error
	Destroy(ctx context.Context, name string) error
	DestroyRecursive(ctx context.Context, dataset string) error
	Rename(ctx context.Context, from, to string) error
	// Replicate is a full single-dataset "zfs send -p <snapshot> | zfs receive
	// <dst>"; props override the received properties.
	Replicate(ctx context.Context, snapshot, dst string, props map[string]string) error
	Get(ctx context.Context, name, prop string) (string, error)
	Set(ctx context.Context, name string, props map[string]string) error
	Inherit(ctx context.Context, name, prop string) error
	Descendants(ctx context.Context, dataset string) ([]string, error)
	Clones(ctx context.Context, snapshot string) ([]string, error)
	Hold(ctx context.Context, snapshot, tag string) error
	Release(ctx context.Context, snapshot, tag string) error
	Holds(ctx context.Context, snapshot string) ([]string, error)
	Mount(ctx context.Context, dataset string) error
	Unmount(ctx context.Context, dataset string) error
}

// Exec runs one command (argv only, never a shell). It returns the output even
// when the command fails.
type Exec interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Files is the filesystem access the builders need. WriteAtomic writes through
// a temp file, fsync and rename.
type Files interface {
	WriteAtomic(ctx context.Context, path string, data []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, path string) error
	RemoveAll(ctx context.Context, path string) error
	SHA256(ctx context.Context, path string) (string, error)
	// SeedKeys copies the host's package signing keys into <root>/usr/share/keys. pkg resolves
	// fingerprint directories inside the root it installs into, so an empty root cannot verify a
	// repository until the keys are there (found on FreeBSD 15.1).
	SeedKeys(ctx context.Context, root string) error
}
