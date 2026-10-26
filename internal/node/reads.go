package node

import (
	"context"
	"errors"
	"io"

	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/sdk/api"
)

// Object is a readable repository object.
type Object struct {
	io.ReadCloser
	// Size is the size of the whole object; Offset and Length describe the part returned.
	Size, Offset, Length int64
}

// SnapshotFile names the readable files of a committed snapshot.
type SnapshotFile string

// Readable snapshot files.
const (
	FileIndex    SnapshotFile = fileIndex
	FileManifest SnapshotFile = fileManif
	FileReceipt  SnapshotFile = fileCommit
)

// Groups lists the groups with their snapshot counts.
func (n *Node) Groups(ctx context.Context) ([]ctl.Group, error) { return n.db.Groups(ctx) }

// Snapshots lists the committed snapshots of a group, oldest first.
func (n *Node) Snapshots(ctx context.Context, group string) ([]ctl.Snapshot, error) {
	if !format.ValidGroupID(group) {
		return nil, errf(api.CodeInvalid, "malformed group id")
	}
	return n.db.Snapshots(ctx, group)
}

// OpenChunk reads a chunk blob, optionally a byte range (length < 0: to the end).
func (n *Node) OpenChunk(ctx context.Context, id format.ID, off, length int64) (*Object, error) {
	if id.IsZero() {
		return nil, errf(api.CodeInvalid, "the zero chunk id has no blob")
	}
	return n.open(ctx, chunkKey(id), off, length)
}

// OpenSnapshotFile reads the index, the manifest or the receipt of a COMMITTED
// snapshot, optionally a byte range. A snapshot without a commit.json does not
// exist, whatever else is lying around.
func (n *Node) OpenSnapshotFile(ctx context.Context, group, snap string, f SnapshotFile, off, length int64) (*Object, error) {
	if !format.ValidGroupID(group) || !format.ValidSnapshotID(snap) {
		return nil, errf(api.CodeInvalid, "malformed group or snapshot id")
	}
	switch f {
	case FileIndex, FileManifest, FileReceipt:
	default:
		return nil, errf(api.CodeInvalid, "unknown snapshot file")
	}
	prefix := snapPrefix(group, snap)
	if _, err := n.be.Head(ctx, prefix+fileCommit); err != nil {
		return nil, asAPIError(err)
	}
	return n.open(ctx, prefix+string(f), off, length)
}

func (n *Node) open(ctx context.Context, key string, off, length int64) (*Object, error) {
	if off < 0 {
		return nil, errf(api.CodeInvalid, "negative offset")
	}
	info, err := n.be.Head(ctx, key)
	if err != nil {
		return nil, asAPIError(err)
	}
	if off > info.Size || off == info.Size && info.Size > 0 {
		return nil, errf(api.CodeInvalid, "the range starts beyond the end of the object")
	}
	want := info.Size - off
	if length >= 0 && length < want {
		want = length
	}
	var rc io.ReadCloser
	if off == 0 && want == info.Size {
		rc, err = n.be.Get(ctx, key)
	} else {
		rc, err = n.be.GetRange(ctx, key, off, want)
	}
	if err != nil {
		return nil, asAPIError(err)
	}
	return &Object{ReadCloser: rc, Size: info.Size, Offset: off, Length: want}, nil
}

// SnapshotState is what a host or a UI may know about a snapshot.
func (n *Node) SnapshotState(ctx context.Context, group, snap string) (ctl.Snapshot, error) {
	s, err := n.db.Snapshot(ctx, group, snap)
	if errors.Is(err, ctl.ErrNotFound) {
		return s, errf(api.CodeNotFound, "no such snapshot")
	}
	return s, err
}

// RepoInfo describes the repository to a host.
type RepoInfo = wire.RepoInfo

// Info returns the repository description.
func (n *Node) Info() RepoInfo {
	return RepoInfo{ID: n.meta.ID, Format: n.meta.Format, Mode: n.be.Mode().String(), Backend: datastore.Unwrap(n.raw).Kind(), ServerID: n.sid, KeyDomain: n.keyDomain(),
		MaxBatch: MaxMissingBatch, MaxBlob: format.MaxBlobSize, Lease: int(n.cfg.Lease.Seconds())}
}

// mayUseGroup is the ownership check on reading a group's snapshots and on reporting a restore test of one. A
// host works on the groups it owns; a host with the restore or admin scope (a recovery host, which holds the key
// but not the groups) works on any. The group must exist: a name nobody ever wrote answers "not found" to all.
func (n *Node) mayUseGroupID(ctx context.Context, h Host, group string) error {
	if !format.ValidGroupID(group) {
		return errf(api.CodeInvalid, "malformed group id")
	}
	return n.mayUseGroup(ctx, h, group)
}

func (n *Node) mayUseGroup(ctx context.Context, h Host, group string) error {
	owner, err := n.db.GroupOwner(ctx, group)
	if err != nil {
		if errors.Is(err, ctl.ErrNotFound) {
			return errf(api.CodeNotFound, "no such group")
		}
		return err
	}
	for _, s := range h.Scopes {
		if s == string(ScopeRestore) || s == string(ScopeAdmin) {
			return nil
		}
	}
	if owner == "" || owner != h.ID {
		return errf(api.CodeNotFound, "no such group") // the same answer as for a group that does not exist
	}
	return nil
}

// GroupsOf lists the groups a host may use: all of them for a recovery or admin host, its own otherwise.
func (n *Node) GroupsOf(ctx context.Context, h Host) ([]ctl.Group, error) {
	all, err := n.db.Groups(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range h.Scopes {
		if s == string(ScopeRestore) || s == string(ScopeAdmin) {
			return all, nil
		}
	}
	var out []ctl.Group
	for _, g := range all {
		if g.HostID != "" && g.HostID == h.ID {
			out = append(out, g)
		}
	}
	return out, nil
}
