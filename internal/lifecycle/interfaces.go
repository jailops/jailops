// Package lifecycle holds the jail lifecycle: plan builders and executors for
// create, start, stop, destroy, snapshot, upgrade, rollback and copy, and the
// rebuild of a registry entry from a jail's own datasets.
//
// Every operation is a plan (internal/plan): an ordered list of steps with
// undo, so that a failure at any step leaves the pool consistent and the jail
// startable on its old generation. The thin jail design (design section 4) is
// a proposal pending prototype 2, so the persistent path list and the merge
// strategy are configuration (Config), not code.
//
// Everything this package assumes about FreeBSD sits behind the interfaces in
// this file and is marked UNVERIFIED where it is used.
package lifecycle

import (
	"context"

	"jailops.net/jailops/sdk/api"
)

// ZFS is the part of ZFS the lifecycle uses. Get returns the local value of a
// property ("" when not set locally). Snapshot is one atomic transaction over
// all names. Replicate is a full single-dataset "zfs send -p <snapshot> | zfs
// receive <dst>": an independent dataset holding that snapshot, children not
// included.
type ZFS interface {
	Exists(ctx context.Context, name string) (bool, error)
	Create(ctx context.Context, name string, props map[string]string) error
	Clone(ctx context.Context, snapshot, target string, props map[string]string) error
	Snapshot(ctx context.Context, props map[string]string, snapshots ...string) error
	Destroy(ctx context.Context, name string) error
	DestroyRecursive(ctx context.Context, dataset string) error
	Rename(ctx context.Context, from, to string) error
	Rollback(ctx context.Context, snapshot string, destroyLater bool) error
	Get(ctx context.Context, name, prop string) (string, error)
	Set(ctx context.Context, name string, props map[string]string) error
	Inherit(ctx context.Context, name, prop string) error
	Descendants(ctx context.Context, dataset string) ([]string, error)
	Snapshots(ctx context.Context, dataset string) ([]string, error)
	Written(ctx context.Context, snapshot string) (int64, error)
	Clones(ctx context.Context, snapshot string) ([]string, error)
	Mount(ctx context.Context, dataset string) error
	Unmount(ctx context.Context, dataset string) error
	Replicate(ctx context.Context, snapshot, dst string, props map[string]string) error
}

// Exec runs one host command (argv only). It returns output even on failure.
type Exec interface {
	Run(ctx context.Context, argv []string) ([]byte, error)
}

// Jails is the jail runtime (jail(8), jexec, kernel queries).
// UNVERIFIED: every implementation detail, see design section 16.
type Jails interface {
	Start(ctx context.Context, name string) error
	// Stop removes the jail; it succeeds when the jail is not running.
	Stop(ctx context.Context, name string) error
	Running(ctx context.Context, name string) (bool, error)
	// Exec runs a command inside a running jail.
	Exec(ctx context.Context, name string, argv []string) ([]byte, error)
	// HostRelease is the running kernel release, for example "15.1-RELEASE".
	HostRelease(ctx context.Context) (string, error)
}

// Files is file access. WriteAtomic writes through a temp file, fsync, rename
// and a directory fsync, keeping the previous content as <path>.previous.
type Files interface {
	WriteAtomic(ctx context.Context, path string, data []byte) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	Exists(ctx context.Context, path string) (bool, error)
	MkdirAll(ctx context.Context, path string) error
	RemoveAll(ctx context.Context, path string) error
	// CopyTree copies a directory tree preserving ownership, modes and flags.
	// UNVERIFIED: flags, ACLs and extended attributes on FreeBSD.
	CopyTree(ctx context.Context, src, dst string) error
}

// Hooks render and remove the derived per-jail configuration (jail.conf,
// in-jail rc.conf, network, firewall anchor). PreStart is idempotent.
type Hooks interface {
	PreStart(ctx context.Context, jail string) error
	PostStop(ctx context.Context, jail string) error
	Remove(ctx context.Context, jail string) error
}

// Images resolves image references. Ensure builds the first generation when
// an image has none. Collect garbage-collects parked generations; it is called
// after a jail is destroyed.
//
// root is the dataset root of the storage the jail lives on: clones need the
// generation on the same pool, so Resolve and Ensure answer with a generation
// that is on root's pool (Ensure builds or receives it there when missing).
type Images interface {
	Resolve(ctx context.Context, root, ref string) (api.ImageGeneration, error)
	Ensure(ctx context.Context, root, ref string) (api.ImageGeneration, error)
	Collect(ctx context.Context) ([]string, error)
}
