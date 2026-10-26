package store

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

// Session is an in-progress snapshot upload and implements the engine's
// Uploader interface in process. Nothing is written for the snapshot itself
// until Commit; chunks uploaded earlier are independent immutable objects.
//
// A session pins every chunk it relies on, the ones the backend said were
// present (Missing) and the ones it uploaded, and at Commit every chunk of the
// index. While it is open the node's garbage collection must not sweep a pinned
// chunk, however old, nor the snapshot the session is based on.
type Session struct {
	s        *Store
	group    string
	snapshot string
	previous string

	mu       sync.Mutex // guards the fields below; never held across backend I/O
	index    []byte
	manifest []byte
	known    map[uint64]struct{}
	closed   bool
	receipt  []byte // the receipt bytes of this commit, fixed at the first attempt
	commitMu sync.Mutex
}

// Begin starts building snapshot for group. previous, if set, names the snapshot
// the new one is derived from; it is pinned while the session is open. A
// snapshot id that is already committed is refused with ErrExists.
func (s *Store) Begin(ctx context.Context, group, snapshot, previous string) (*Session, error) {
	if err := checkIDs(group, snapshot); err != nil {
		return nil, err
	}
	if previous != "" && !format.ValidSnapshotID(previous) {
		return nil, fmt.Errorf("%w: previous snapshot id", ErrInvalid)
	}
	if ok, err := datastore.Exists(ctx, s.b, SnapPrefix(group, snapshot)+fileCommit); err != nil {
		return nil, err
	} else if ok {
		return nil, ErrExists
	}
	x := &Session{s: s, group: group, snapshot: snapshot, previous: previous, known: map[uint64]struct{}{}}
	s.pinMu.Lock()
	s.sessions[x] = struct{}{}
	s.pinMu.Unlock()
	return x, nil
}

func pinKey(id format.ID) uint64 {
	return uint64(id[0]) | uint64(id[1])<<8 | uint64(id[2])<<16 | uint64(id[3])<<24 |
		uint64(id[4])<<32 | uint64(id[5])<<40 | uint64(id[6])<<48 | uint64(id[7])<<56
}

// Pinned reports whether an open session relies on the chunk. Pins are kept by
// the first 8 bytes of the id: a collision can only keep a chunk longer.
func (s *Store) Pinned(id format.ID) bool {
	k := pinKey(id)
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	for x := range s.sessions {
		x.mu.Lock()
		_, ok := x.known[k]
		x.mu.Unlock()
		if ok {
			return true
		}
	}
	return false
}

// PinnedSnapshot reports whether an open session is based on the snapshot or is
// building it.
func (s *Store) PinnedSnapshot(group, snap string) bool {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	for x := range s.sessions {
		if x.group == group && (x.previous == snap || x.snapshot == snap) {
			return true
		}
	}
	return false
}

// OpenSessions returns the number of open sessions.
func (s *Store) OpenSessions() int {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	return len(s.sessions)
}

func (x *Session) pin(ids ...format.ID) error {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return ErrSessionClosed
	}
	for _, id := range ids {
		x.known[pinKey(id)] = struct{}{}
	}
	return nil
}

// Missing returns the ids the repository lacks and pins the ones it has.
func (x *Session) Missing(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	x.mu.Lock()
	closed := x.closed
	x.mu.Unlock()
	if closed {
		return nil, ErrSessionClosed
	}
	miss, err := x.s.Missing(ctx, ids)
	if err != nil {
		return nil, err
	}
	gone := make(map[format.ID]struct{}, len(miss))
	for _, m := range miss {
		gone[m] = struct{}{}
	}
	var have []format.ID
	for _, id := range ids {
		if _, no := gone[id]; !no && !id.IsZero() {
			have = append(have, id)
		}
	}
	if err := x.pin(have...); err != nil {
		return nil, err
	}
	return miss, nil
}

// PutChunk stores a chunk blob (idempotent) and pins it.
func (x *Session) PutChunk(ctx context.Context, id format.ID, blob []byte) error {
	if err := x.pin(id); err != nil {
		return err
	}
	_, err := x.s.PutChunk(ctx, id, blob)
	return err
}

// PutIndex checks and keeps the index file until Commit.
func (x *Session) PutIndex(ctx context.Context, b []byte) error {
	if _, err := format.ParseIndex(b); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return ErrSessionClosed
	}
	x.index = append([]byte(nil), b...)
	return nil
}

// PutManifest checks the encrypted manifest's trailer and keeps it until Commit.
func (x *Session) PutManifest(ctx context.Context, b []byte) error {
	if err := format.VerifyManifestBlob(b); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.closed {
		return ErrSessionClosed
	}
	x.manifest = append([]byte(nil), b...)
	return nil
}

// Close ends the session and releases its pins. Idempotent.
func (x *Session) Close() {
	x.commitMu.Lock()
	defer x.commitMu.Unlock()
	x.close()
}

func (x *Session) close() {
	x.s.pinMu.Lock()
	defer x.s.pinMu.Unlock()
	x.mu.Lock()
	x.closed = true
	x.known = map[uint64]struct{}{}
	x.mu.Unlock()
	delete(x.s.sessions, x)
}

// Abort discards the session (nothing was written for the snapshot).
func (x *Session) Abort() { x.Close() }

// Commit makes the snapshot visible. Order: pin every chunk of the index, ask
// the backend (never a cache) that every chunk exists, write the index, then the
// manifest, then commit.json, last and create-only. A commit that finds chunks
// missing returns ErrMissingChunks and leaves the session open for a retry. A
// retried commit of identical bytes, after a lost response or a crash after the
// marker, is success and returns the stored receipt. The session is closed after
// a successful commit.
func (x *Session) Commit(ctx context.Context) (*format.Receipt, error) {
	rc, err := x.commit(ctx)
	if err == nil || rc != nil || errors.Is(err, ErrExists) {
		x.Close()
	}
	return rc, err
}

func (x *Session) commit(ctx context.Context) (*format.Receipt, error) {
	x.commitMu.Lock()
	defer x.commitMu.Unlock()
	x.mu.Lock()
	closed, indexB, manifB := x.closed, x.index, x.manifest
	x.mu.Unlock()
	if closed {
		return nil, ErrSessionClosed
	}
	if indexB == nil || manifB == nil {
		return nil, fmt.Errorf("%w: index and manifest are required", ErrInvalid)
	}
	if err := x.s.at("commit:start"); err != nil {
		return nil, err
	}
	idx, err := format.ParseIndex(indexB)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ids := idx.ChunkIDs()
	// 1. pin every chunk of the index before looking at any of them
	if err := x.pin(ids...); err != nil {
		return nil, err
	}
	if err := x.s.at("commit:pinned"); err != nil {
		return nil, err
	}
	// 2. every chunk must exist: ask the backend about each distinct chunk
	present, err := x.s.headChunks(ctx, ids)
	if err != nil {
		return nil, err
	}
	var missing []format.ID
	for i, id := range ids {
		if !present[i] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %d (first %s)", ErrMissingChunks, len(missing), missing[0])
	}
	if err := x.s.at("commit:checked"); err != nil {
		return nil, err
	}
	prefix := SnapPrefix(x.group, x.snapshot)
	l := x.s.snapLock(x.group, x.snapshot)
	l.Lock()
	defer l.Unlock()

	// the receipt is computed once per session, so a retry writes identical bytes
	if x.receipt == nil {
		rc := &format.Receipt{Committed: x.s.now().UTC(), ChunkCount: len(ids), IndexBytes: len(indexB),
			IndexDigest: format.FileDigest(indexB), ManifestDigest: format.FileDigest(manifB), Source: "client"}
		x.receipt, err = rc.Marshal()
		if err != nil {
			return nil, err
		}
	}
	if done, rc, err := x.existingMarker(ctx, prefix); err != nil || done {
		return rc, err
	}
	// 3. index, then manifest. Create-only first; the debris of an earlier,
	// uncommitted attempt (no marker) is replaced, which is safe because the
	// snapshot does not exist until the marker does.
	if err := x.putDebrisSafe(ctx, prefix+fileIndex, indexB); err != nil {
		return nil, err
	}
	if err := x.s.at("commit:after-index"); err != nil {
		return nil, err
	}
	if err := x.putDebrisSafe(ctx, prefix+fileManif, manifB); err != nil {
		return nil, err
	}
	if err := x.s.at("commit:before-marker"); err != nil {
		return nil, err
	}
	// 4. the marker, last and create-only
	err = datastore.PutBytes(ctx, x.s.b, prefix+fileCommit, x.receipt, datastore.PutOptions{IfAbsent: true})
	if err != nil {
		if errors.Is(err, datastore.ErrExists) {
			if done, rc, err := x.existingMarker(ctx, prefix); err != nil || done {
				return rc, err
			}
			return nil, ErrExists
		}
		return nil, err
	}
	rc, perr := format.ParseReceipt(x.receipt)
	if err := x.s.at("commit:after-marker"); err != nil {
		return rc, err // committed: the caller may treat this as a lost response and retry
	}
	return rc, perr
}

// existingMarker looks at commit.json: absent means carry on; present with the
// digests of this session is an idempotent retry (success); anything else is
// ErrExists.
func (x *Session) existingMarker(ctx context.Context, prefix string) (done bool, rc *format.Receipt, err error) {
	b, err := datastore.ReadAll(ctx, x.s.b, prefix+fileCommit, maxReceiptBytes)
	if errors.Is(err, datastore.ErrNotFound) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	cur, perr := format.ParseReceipt(b)
	mine, _ := format.ParseReceipt(x.receipt)
	if perr == nil && mine != nil && cur.IndexDigest == mine.IndexDigest && cur.ManifestDigest == mine.ManifestDigest {
		return true, cur, nil
	}
	return true, nil, ErrExists
}

// putDebrisSafe writes an object of an uncommitted snapshot: create-only, and if
// the key holds the debris of an earlier attempt, replace it.
func (x *Session) putDebrisSafe(ctx context.Context, key string, data []byte) error {
	err := datastore.PutBytes(ctx, x.s.b, key, data, datastore.PutOptions{IfAbsent: true})
	if !errors.Is(err, datastore.ErrExists) {
		return err
	}
	cur, gerr := datastore.ReadAll(ctx, x.s.b, key, format.MaxIndexSize)
	if gerr == nil && equalBytes(cur, data) {
		return nil
	}
	return datastore.PutBytes(ctx, x.s.b, key, data, datastore.PutOptions{})
}
