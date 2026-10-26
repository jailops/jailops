package engine

import (
	"context"
	"errors"
	"fmt"

	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/store"
)

// StoreRepo adapts a Store to Repository (the in-process read side).
type StoreRepo struct{ S *store.Store }

func (r StoreRepo) mapErr(err error) error {
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %v", ErrNoSnapshot, err)
	}
	return err
}

// ListSnapshots implements Repository. A snapshot whose verify.json says it
// failed is marked Failed.
func (r StoreRepo) ListSnapshots(ctx context.Context, group string) ([]SnapshotMeta, error) {
	l, err := r.S.ListSnapshots(ctx, group)
	if err != nil {
		return nil, err
	}
	out := make([]SnapshotMeta, 0, len(l))
	for _, si := range l {
		m := SnapshotMeta{ID: si.Snapshot, Committed: si.Receipt.Committed}
		if si.HasVerify {
			v, err := r.S.ReadVerify(ctx, group, si.Snapshot)
			if err != nil {
				return nil, err
			}
			m.Failed = v != nil && v.Failed
		}
		out = append(out, m)
	}
	return out, nil
}

// ReadIndex implements Repository.
func (r StoreRepo) ReadIndex(ctx context.Context, group, snap string) ([]byte, error) {
	b, err := r.S.ReadIndex(ctx, group, snap)
	return b, r.mapErr(err)
}

// ReadManifest implements Repository.
func (r StoreRepo) ReadManifest(ctx context.Context, group, snap string) ([]byte, error) {
	b, err := r.S.ReadManifest(ctx, group, snap)
	return b, r.mapErr(err)
}

// ReadReceipt implements Repository.
func (r StoreRepo) ReadReceipt(ctx context.Context, group, snap string) (*format.Receipt, error) {
	rc, err := r.S.ReadReceipt(ctx, group, snap)
	return rc, r.mapErr(err)
}

// GetChunk implements Repository.
func (r StoreRepo) GetChunk(ctx context.Context, id format.ID) ([]byte, error) {
	return r.S.GetChunk(ctx, id)
}

// KeyDomain implements Repository.
func (r StoreRepo) KeyDomain() string { return r.S.Meta().KeyDomain }

// StoreUploader returns the NewUploader function of BackupOptions for an
// in-process Store: *store.Session implements Uploader.
func StoreUploader(s *store.Store) func(ctx context.Context, group, snapshot, previous string) (Uploader, error) {
	return func(ctx context.Context, group, snapshot, previous string) (Uploader, error) {
		return s.Begin(ctx, group, snapshot, previous)
	}
}

var _ Uploader = (*store.Session)(nil)
