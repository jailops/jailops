package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

// Missing returns the ids the repository does not hold, in the order of first
// occurrence, asking the backend itself (HEAD, in parallel) for each distinct
// id. It writes nothing and pins nothing; a Session's Missing pins the chunks
// it finds present. The zero id is never reported.
func (s *Store) Missing(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	uniq := uniqueIDs(ids)
	present, err := s.headChunks(ctx, uniq)
	if err != nil {
		return nil, err
	}
	var out []format.ID
	for i, id := range uniq {
		if !present[i] {
			out = append(out, id)
		}
	}
	return out, nil
}

func uniqueIDs(ids []format.ID) []format.ID {
	seen := make(map[format.ID]struct{}, len(ids))
	out := make([]format.ID, 0, len(ids))
	for _, id := range ids {
		if id.IsZero() {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// headChunks asks the backend whether each chunk exists. Never from a cache.
func (s *Store) headChunks(ctx context.Context, ids []format.ID) ([]bool, error) {
	present := make([]bool, len(ids))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, CheckParallelism)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for i := range ids {
		sem <- struct{}{}
		if cctx.Err() != nil {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := s.b.Head(cctx, ChunkKey(ids[i]))
			switch {
			case err == nil:
				present[i] = true
			case errors.Is(err, datastore.ErrNotFound):
			default:
				mu.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return present, nil
}

// PutChunk stores a blob under id. The trailer is verified (keyless); an
// existing chunk is left alone (create-only). created reports a new object. A
// chunk that was quarantined as corrupt does not exist any more, so putting it
// again heals it.
func (s *Store) PutChunk(ctx context.Context, id format.ID, blob []byte) (created bool, err error) {
	if id.IsZero() {
		return false, fmt.Errorf("%w: the zero id has no blob", ErrInvalid)
	}
	if err := format.VerifyBlob(blob); err != nil {
		return false, fmt.Errorf("%w: rejecting blob %s: %v", ErrInvalid, id, err)
	}
	if err := s.at("chunk:before-write"); err != nil {
		return false, err
	}
	err = datastore.PutBytes(ctx, s.b, ChunkKey(id), blob, datastore.PutOptions{IfAbsent: true})
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, datastore.ErrExists):
		return false, nil
	}
	return false, err
}

// GetChunk returns the stored blob.
func (s *Store) GetChunk(ctx context.Context, id format.ID) ([]byte, error) {
	data, err := datastore.ReadAll(ctx, s.b, ChunkKey(id), format.MaxBlobSize)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, ErrNotFound
	}
	return data, err
}

// HasChunk reports whether the chunk object exists.
func (s *Store) HasChunk(ctx context.Context, id format.ID) (bool, error) {
	return datastore.Exists(ctx, s.b, ChunkKey(id))
}

// ChunkHealthy reports whether the chunk exists and its blob passes the keyless
// integrity check.
func (s *Store) ChunkHealthy(ctx context.Context, id format.ID) bool {
	b, err := s.GetChunk(ctx, id)
	return err == nil && format.VerifyBlob(b) == nil
}

// ListChunks calls fn for every chunk object, in key order.
func (s *Store) ListChunks(ctx context.Context, fn func(id format.ID, info datastore.ObjectInfo) error) error {
	return s.b.List(ctx, "chunks/", func(o datastore.ObjectInfo) error {
		id, err := format.ParseID(o.Key[strings.LastIndex(o.Key, "/")+1:])
		if err != nil || ChunkKey(id) != o.Key {
			return nil // not a chunk object: ignore
		}
		return fn(id, o)
	})
}

// ---- quarantine -----------------------------------------------------------------

// Corrupt chunks are quarantined, not merely reported: the blob is moved to
// quarantine/<id> and removed from chunks/, so from that moment it no longer
// answers "present" to a dedup check, and the next backup that holds the same
// plaintext uploads a fresh healthy copy. The bad blob is kept for inspection
// until the chunk is healthy again and PurgeQuarantine runs.

// Quarantine moves a chunk blob out of the live chunk namespace. It is
// idempotent. A blob that passes the keyless check is not moved (a concurrent
// heal may have replaced it).
func (s *Store) Quarantine(ctx context.Context, id format.ID) error {
	blob, err := datastore.ReadAll(ctx, s.b, ChunkKey(id), format.MaxBlobSize)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if format.VerifyBlob(blob) == nil {
		return nil
	}
	return s.quarantineBlob(ctx, id, blob)
}

// QuarantineKeyed moves a chunk out regardless of its trailer: a holder of the
// key found that it does not decrypt or does not match its id (poisoning).
func (s *Store) QuarantineKeyed(ctx context.Context, id format.ID) error {
	blob, err := datastore.ReadAll(ctx, s.b, ChunkKey(id), format.MaxBlobSize)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.quarantineBlob(ctx, id, blob)
}

func (s *Store) quarantineBlob(ctx context.Context, id format.ID, blob []byte) error {
	if err := s.at("quarantine:before-copy"); err != nil {
		return err
	}
	// quarantine/<id> is replaceable: a second corruption of the same id overwrites the first
	if err := datastore.PutBytes(ctx, s.b, quarantineKey(id), blob, datastore.PutOptions{}); err != nil {
		return err
	}
	if err := s.at("quarantine:before-delete"); err != nil {
		return err
	}
	return s.b.Delete(ctx, ChunkKey(id))
}

// QuarantinedChunk is an entry of the quarantine area.
type QuarantinedChunk struct {
	ID     format.ID
	Size   int64
	Since  time.Time
	Healed bool // a healthy copy is in chunks/ again
}

// ListQuarantine returns the quarantined chunks.
func (s *Store) ListQuarantine(ctx context.Context) ([]QuarantinedChunk, error) {
	var out []QuarantinedChunk
	err := s.b.List(ctx, "quarantine/", func(o datastore.ObjectInfo) error {
		id, perr := format.ParseID(strings.TrimPrefix(o.Key, "quarantine/"))
		if perr != nil {
			return nil
		}
		out = append(out, QuarantinedChunk{ID: id, Size: o.Size, Since: o.ModTime})
		return nil
	})
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Healed = s.ChunkHealthy(ctx, out[i].ID)
	}
	return out, nil
}

// PurgeQuarantine deletes quarantined blobs whose chunk is healthy again and
// that were quarantined more than olderThan ago. It returns how many it removed.
func (s *Store) PurgeQuarantine(ctx context.Context, olderThan time.Duration) (int, error) {
	q, err := s.ListQuarantine(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range q {
		if c.Healed && s.now().Sub(c.Since) > olderThan {
			if s.b.Delete(ctx, quarantineKey(c.ID)) == nil {
				n++
			}
		}
	}
	return n, nil
}
