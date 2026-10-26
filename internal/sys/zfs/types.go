package zfs

import (
	"context"
	"io"
	"time"
)

// Dataset is one row of a listing.
type Dataset struct {
	Name string
	// Type is "filesystem", "volume", "snapshot" or "bookmark".
	Type string
	// Props holds the requested properties. A property that is not set
	// (zfs prints "-") is absent from the map.
	Props map[string]string
}

// Prop is one property with its source ("local", "inherited from x",
// "default", "-", ...).
type Prop struct {
	Value  string
	Source string
}

// ListOpts selects datasets for List.
type ListOpts struct {
	// Root limits the listing to this dataset (and below when Recursive).
	Root string
	// Types: any of "filesystem", "volume", "snapshot", "bookmark".
	// Empty means filesystem and volume.
	Types     []string
	Recursive bool
	// Depth > 0 limits recursion (zfs -d). It implies recursion.
	Depth int
	// Props are the extra properties to read, including user properties.
	Props []string
}

// CreateOpts for Create.
type CreateOpts struct {
	Name string
	// VolumeSize, when non-empty (e.g. "10G" or a byte count), creates a volume.
	VolumeSize string
	// Parents creates missing parents (-p).
	Parents bool
	Props   map[string]string
}

// SnapshotOpts for Snapshot. All names are snapshotted in one atomic
// `zfs snapshot` call and must live in the same pool.
type SnapshotOpts struct {
	Names     []string // dataset@snap
	Recursive bool
	Props     map[string]string
}

// DestroyOpts for Destroy.
type DestroyOpts struct {
	// Defer (-d) marks a snapshot for deferred destruction.
	Defer bool
	// Recursive (-r) destroys children and snapshots.
	Recursive bool
	// Dependents (-R) also destroys clones and other dependents.
	Dependents bool
}

// RenameOpts for Rename.
type RenameOpts struct {
	// Parents (-p) creates missing parents of the target.
	Parents bool
	// Recursive (-r) renames snapshots of descendants.
	Recursive bool
}

// RollbackOpts for Rollback.
type RollbackOpts struct {
	// DestroyNewer (-r) destroys snapshots more recent than the target.
	DestroyNewer bool
	// DestroyClones (-R) also destroys clones of those snapshots.
	DestroyClones bool
}

// Hold is one user hold on a snapshot.
type Hold struct {
	Tag     string
	Created time.Time
}

// SendOpts selects a send stream.
type SendOpts struct {
	// Snapshot is the (final) snapshot to send. Ignored when ResumeToken is set.
	Snapshot string
	// From, when set, makes the stream incremental from this snapshot or
	// bookmark (full name, same dataset lineage).
	From string
	// Intermediary (-I) includes the snapshots between From and Snapshot.
	Intermediary bool
	// Raw (-w) sends encrypted datasets as stored, without keys.
	Raw        bool
	Compressed bool // -c
	LargeBlock bool // -L
	Replicate  bool // -R (replication stream including properties)
	// ResumeToken (-t) resumes an interrupted receive.
	ResumeToken string
}

// ReceiveOpts selects receive behaviour. Nothing here is on by default
// except -u (never mount received datasets).
type ReceiveOpts struct {
	Target string
	// Resumable (-s) keeps partial state when the stream breaks.
	Resumable bool
	// Force (-F) rolls the target back and may destroy snapshots. It must
	// be set explicitly; JailOps never uses it for backup chains.
	Force bool
	// Props sets properties on the received dataset (-o).
	Props map[string]string
	// Exclude lists properties not to be received (-x).
	Exclude []string
	// Mount lets zfs mount the received dataset (omits -u).
	Mount bool
}

// Pool is one row of `zpool list`.
type Pool struct {
	Name      string
	Size      uint64
	Allocated uint64
	Free      uint64
	Health    string
}

// Client is the ZFS interface used by the rest of JailOps. Implementations
// are NewClient (over run.Runner) and Fake.
type Client interface {
	List(ctx context.Context, o ListOpts) ([]Dataset, error)
	// Get reads properties of one dataset, snapshot or bookmark.
	Get(ctx context.Context, name string, props ...string) (map[string]Prop, error)
	Exists(ctx context.Context, name string) (bool, error)
	Set(ctx context.Context, name string, props map[string]string) error
	Inherit(ctx context.Context, name string, props ...string) error
	Create(ctx context.Context, o CreateOpts) error
	// Clone creates target from snapshot.
	Clone(ctx context.Context, snapshot, target string, props map[string]string, parents bool) error
	Snapshot(ctx context.Context, o SnapshotOpts) error
	Destroy(ctx context.Context, name string, o DestroyOpts) error
	Rename(ctx context.Context, from, to string, o RenameOpts) error
	Rollback(ctx context.Context, snapshot string, o RollbackOpts) error
	Hold(ctx context.Context, tag string, snapshots ...string) error
	Release(ctx context.Context, tag string, snapshots ...string) error
	Holds(ctx context.Context, snapshot string) ([]Hold, error)
	Bookmark(ctx context.Context, snapshot, bookmark string) error
	// Written returns bytes written to dataset since snapshot (a full
	// snapshot name), or since its latest snapshot when since is "".
	Written(ctx context.Context, dataset, since string) (uint64, error)
	// GUID of a dataset, snapshot or bookmark.
	GUID(ctx context.Context, name string) (uint64, error)
	// Clones of a snapshot.
	Clones(ctx context.Context, snapshot string) ([]string, error)
	// Origin of a clone, or "" if the dataset is not a clone.
	Origin(ctx context.Context, dataset string) (string, error)
	// Send streams to w. Cancel ctx to abort.
	Send(ctx context.Context, o SendOpts, w io.Writer) error
	Receive(ctx context.Context, o ReceiveOpts, r io.Reader) error
	// ReceiveAbort discards the partial state of a resumable receive (-A).
	ReceiveAbort(ctx context.Context, dataset string) error
	ListPools(ctx context.Context) ([]Pool, error)
	// ActiveFeatures lists feature flags in state "active" (short names,
	// without the "feature@" prefix). Enabled-but-unused features are omitted.
	ActiveFeatures(ctx context.Context, pool string) ([]string, error)
}
