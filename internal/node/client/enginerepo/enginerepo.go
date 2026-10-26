// Package enginerepo adapts the client of the backup node to the interfaces of
// the backup engine (internal/repo/engine): the write side of one snapshot
// (engine.Uploader, opened with NewUploader) and the read side
// (engine.Repository). The host daemon imports this package; the backup node
// binary never does.
package enginerepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"jailops.net/jailops/internal/node/client"
	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/store"
	"jailops.net/jailops/sdk/api"
)

// Repo is the read side of a remote repository.
type Repo struct {
	C *client.Client
	// Domain is the key domain id reported by KeyDomain; the node itself does not know the key.
	Domain string
}

var (
	_ engine.Repository = (*Repo)(nil)
)

func mapErr(err error) error {
	if api.IsCode(err, api.CodeNotFound) {
		return fmt.Errorf("%w: %v", engine.ErrNoSnapshot, err)
	}
	return err
}

// ListSnapshots implements engine.Repository. A snapshot the node's verification marked failed is marked Failed.
func (r *Repo) ListSnapshots(ctx context.Context, group string) ([]engine.SnapshotMeta, error) {
	l, err := r.C.Snapshots(ctx, group)
	if err != nil {
		return nil, err
	}
	out := make([]engine.SnapshotMeta, len(l))
	for i, s := range l {
		out[i] = engine.SnapshotMeta{ID: s.SnapshotID, Committed: s.CommittedAt, Failed: s.VerifyState == "failed"}
	}
	return out, nil
}

// ReadIndex implements engine.Repository.
func (r *Repo) ReadIndex(ctx context.Context, group, snap string) ([]byte, error) {
	rc, _, err := r.C.Index(ctx, group, snap, 0, -1)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, format.MaxIndexSize+1))
}

// ReadManifest implements engine.Repository.
func (r *Repo) ReadManifest(ctx context.Context, group, snap string) ([]byte, error) {
	b, err := r.C.Manifest(ctx, group, snap)
	return b, mapErr(err)
}

// ReadReceipt implements engine.Repository.
func (r *Repo) ReadReceipt(ctx context.Context, group, snap string) (*format.Receipt, error) {
	rc, err := r.C.Receipt(ctx, group, snap)
	return rc, mapErr(err)
}

// GetChunk implements engine.Repository.
func (r *Repo) GetChunk(ctx context.Context, id format.ID) ([]byte, error) { return r.C.Chunk(ctx, id) }

// KeyDomain implements engine.Repository.
func (r *Repo) KeyDomain() string { return r.Domain }

// HasEscrow implements keys.EscrowChecker.
func (r *Repo) HasEscrow(ctx context.Context, domain string) (bool, error) {
	return r.C.HasEscrow(ctx, domain)
}

// PutEscrow implements keys.EscrowStore.
func (r *Repo) PutEscrow(ctx context.Context, domain string, blob []byte) error {
	return r.C.PutEscrow(ctx, domain, blob)
}

// NewUploader returns the engine's BackupOptions.NewUploader for a remote repository.
func NewUploader(c *client.Client) func(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error) {
	return func(ctx context.Context, group, snapshot, previous string) (engine.Uploader, error) {
		s, err := c.Begin(ctx, group, snapshot, previous)
		if err != nil {
			return nil, err
		}
		return &uploader{s: s}, nil
	}
}

type uploader struct {
	s    client.UploadSession
	mu   sync.Mutex
	done bool
}

var _ engine.Uploader = (*uploader)(nil)

func (u *uploader) Missing(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	return u.s.Missing(ctx, ids)
}
func (u *uploader) PutChunk(ctx context.Context, id format.ID, blob []byte) error {
	return u.s.PutChunk(ctx, id, blob)
}
func (u *uploader) PutIndex(ctx context.Context, data []byte) error { return u.s.PutIndex(ctx, data) }
func (u *uploader) PutManifest(ctx context.Context, data []byte) error {
	return u.s.PutManifest(ctx, data)
}

// Commit returns an error wrapping store.ErrMissingChunks when the node lacks chunks.
func (u *uploader) Commit(ctx context.Context) (*format.Receipt, error) {
	rc, err := u.s.Commit(ctx)
	var em *client.ErrMissingChunks
	if errors.As(err, &em) {
		return nil, fmt.Errorf("%w: %d (first %v)", store.ErrMissingChunks, em.Count, em.First)
	}
	if err == nil {
		u.mu.Lock()
		u.done = true
		u.mu.Unlock()
	}
	return rc, err
}

// Close aborts a session that was not committed.
func (u *uploader) Close() {
	u.mu.Lock()
	done := u.done
	u.done = true
	u.mu.Unlock()
	if done {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = u.s.Abort(ctx)
}
