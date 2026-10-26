package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

// SnapshotInfo describes a committed snapshot.
type SnapshotInfo struct {
	Group     string
	Snapshot  string
	Receipt   format.Receipt
	Protected bool
	HasNotes  bool
	HasVerify bool
}

// ListGroups returns every group id that has at least one object.
func (s *Store) ListGroups(ctx context.Context) ([]string, error) {
	seen := map[string]struct{}{}
	err := s.b.List(ctx, groupPrefix, func(o datastore.ObjectInfo) error {
		rest := strings.TrimPrefix(o.Key, groupPrefix)
		if i := strings.IndexByte(rest, '/'); i > 0 && ValidGroup(rest[:i]) {
			seen[rest[:i]] = struct{}{}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(seen))
	for g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	return out, nil
}

type snapFlags struct{ marker, protected, notes, verify bool }

// scanGroup lists the objects of a group once and returns what each snapshot
// directory holds.
func (s *Store) scanGroup(ctx context.Context, group string) (map[string]*snapFlags, error) {
	if !ValidGroup(group) {
		return nil, fmt.Errorf("%w: invalid group", ErrInvalid)
	}
	seen := map[string]*snapFlags{}
	prefix := groupPrefix + group + "/"
	err := s.b.List(ctx, prefix, func(o datastore.ObjectInfo) error {
		rest := strings.TrimPrefix(o.Key, prefix)
		i := strings.IndexByte(rest, '/')
		if i <= 0 || !format.ValidSnapshotID(rest[:i]) {
			return nil
		}
		f := seen[rest[:i]]
		if f == nil {
			f = &snapFlags{}
			seen[rest[:i]] = f
		}
		switch rest[i+1:] {
		case fileCommit:
			f.marker = true
		case fileProt:
			f.protected = true
		case fileNotes:
			f.notes = true
		case fileVerify:
			f.verify = true
		}
		return nil
	})
	return seen, err
}

// ListSnapshots returns the COMMITTED snapshots of a group, oldest first.
// Uploads without a commit marker are not snapshots and are not listed.
func (s *Store) ListSnapshots(ctx context.Context, group string) ([]SnapshotInfo, error) {
	seen, err := s.scanGroup(ctx, group)
	if err != nil {
		return nil, err
	}
	var ids []string
	for sn, f := range seen {
		if f.marker {
			ids = append(ids, sn)
		}
	}
	sort.Strings(ids)
	infos := make([]SnapshotInfo, len(ids))
	gone := make([]bool, len(ids))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	sem := make(chan struct{}, 16)
	for i, sn := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f := seen[sn]
			si := SnapshotInfo{Group: group, Snapshot: sn, Protected: f.protected, HasNotes: f.notes, HasVerify: f.verify}
			b, err := datastore.ReadAll(ctx, s.b, SnapPrefix(group, sn)+fileCommit, maxReceiptBytes)
			switch {
			case errors.Is(err, datastore.ErrNotFound):
				gone[i] = true // deleted while listing
			case err != nil:
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
			default:
				rc, perr := format.ParseReceipt(b)
				if perr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("snapshot %s/%s: %w", group, sn, perr)
					}
					mu.Unlock()
				} else {
					si.Receipt = *rc
				}
			}
			infos[i] = si
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	out := make([]SnapshotInfo, 0, len(ids))
	for i := range infos {
		if !gone[i] {
			out = append(out, infos[i])
		}
	}
	return out, nil
}

// SnapshotExists reports whether a committed snapshot is in the repository.
func (s *Store) SnapshotExists(ctx context.Context, group, snap string) (bool, error) {
	if err := checkIDs(group, snap); err != nil {
		return false, err
	}
	return datastore.Exists(ctx, s.b, SnapPrefix(group, snap)+fileCommit)
}

// ReadReceipt returns the receipt of a committed snapshot.
func (s *Store) ReadReceipt(ctx context.Context, group, snap string) (*format.Receipt, error) {
	b, err := s.readSnap(ctx, group, snap, fileCommit, maxReceiptBytes)
	if err != nil {
		return nil, err
	}
	return format.ParseReceipt(b)
}

// ReadIndex returns the raw index file of a committed snapshot.
func (s *Store) ReadIndex(ctx context.Context, group, snap string) ([]byte, error) {
	return s.readSnap(ctx, group, snap, fileIndex, format.MaxIndexSize)
}

// ReadManifest returns the raw manifest blob of a committed snapshot.
func (s *Store) ReadManifest(ctx context.Context, group, snap string) ([]byte, error) {
	return s.readSnap(ctx, group, snap, fileManif, maxManifestBytes)
}

func (s *Store) readSnap(ctx context.Context, group, snap, file string, max int64) ([]byte, error) {
	if err := checkIDs(group, snap); err != nil {
		return nil, err
	}
	// a snapshot that is not committed does not exist, whatever else is lying around
	ok, err := datastore.Exists(ctx, s.b, SnapPrefix(group, snap)+fileCommit)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	b, err := datastore.ReadAll(ctx, s.b, SnapPrefix(group, snap)+file, max)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, ErrNotFound
	}
	return b, err
}

// Protect sets or clears the protected marker of a committed snapshot.
func (s *Store) Protect(ctx context.Context, group, snap string, on bool) error {
	if err := checkIDs(group, snap); err != nil {
		return err
	}
	l := s.snapLock(group, snap)
	l.Lock()
	defer l.Unlock()
	ok, err := datastore.Exists(ctx, s.b, SnapPrefix(group, snap)+fileCommit)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	key := SnapPrefix(group, snap) + fileProt
	if !on {
		return s.b.Delete(ctx, key)
	}
	err = datastore.PutBytes(ctx, s.b, key, []byte("1"), datastore.PutOptions{IfAbsent: true})
	if errors.Is(err, datastore.ErrExists) {
		return nil
	}
	return err
}

// DeleteSnapshot removes a committed snapshot; its chunks are left for garbage
// collection. The commit marker goes first, so the snapshot disappears
// atomically; a crash after that leaves only debris. A protected snapshot is
// refused with ErrProtected.
func (s *Store) DeleteSnapshot(ctx context.Context, group, snap string) error {
	if err := checkIDs(group, snap); err != nil {
		return err
	}
	prefix := SnapPrefix(group, snap)
	l := s.snapLock(group, snap)
	l.Lock()
	defer l.Unlock()
	marker, err := datastore.ReadAll(ctx, s.b, prefix+fileCommit, maxReceiptBytes)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return ErrNotFound
		}
		return err
	}
	if ok, err := datastore.Exists(ctx, s.b, prefix+fileProt); err != nil {
		return err
	} else if ok {
		return ErrProtected
	}
	if err := s.at("delete:before-marker"); err != nil {
		return err
	}
	if err := s.b.Delete(ctx, prefix+fileCommit); err != nil {
		return err
	}
	if err := s.at("delete:after-marker"); err != nil {
		return err
	}
	// protection set between the check and the delete wins: put the marker back
	if ok, err := datastore.Exists(ctx, s.b, prefix+fileProt); err == nil && ok {
		if perr := datastore.PutBytes(ctx, s.b, prefix+fileCommit, marker, datastore.PutOptions{IfAbsent: true}); perr != nil && !errors.Is(perr, datastore.ErrExists) {
			return perr
		}
		return ErrProtected
	}
	for _, f := range []string{fileIndex, fileManif, fileNotes, fileVerify, fileProt} {
		if err := s.at("delete:file:" + f); err != nil {
			return err
		}
		if err := s.b.Delete(ctx, prefix+f); err != nil {
			return err
		}
	}
	return nil
}

// CleanOrphans removes the objects of uploads that never got their commit
// marker (index, manifest, and stray metadata of a snapshot whose delete
// stopped half-way) once they are older than olderThan. Only keys of the form
// groups/<group>/<snapshot>/<file> are considered.
func (s *Store) CleanOrphans(ctx context.Context, olderThan time.Duration) (int, error) {
	cutoff := s.now().Add(-olderThan)
	committed := map[string]bool{}
	type obj struct {
		dir, key string
		mod      time.Time
	}
	var all []obj
	err := s.b.List(ctx, groupPrefix, func(o datastore.ObjectInfo) error {
		parts := strings.Split(o.Key, "/")
		if len(parts) != 4 || !ValidGroup(parts[1]) || !format.ValidSnapshotID(parts[2]) {
			return nil
		}
		dir := strings.Join(parts[:3], "/") + "/"
		if parts[3] == fileCommit {
			committed[dir] = true
		} else {
			all = append(all, obj{dir, o.Key, o.ModTime})
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	byDir := map[string][]obj{}
	var dirs []string
	for _, o := range all {
		if committed[o.dir] || o.mod.After(cutoff) {
			continue
		}
		if byDir[o.dir] == nil {
			dirs = append(dirs, o.dir)
		}
		byDir[o.dir] = append(byDir[o.dir], o)
	}
	n := 0
	for _, dir := range dirs {
		parts := strings.Split(dir, "/")
		if s.PinnedSnapshot(parts[1], parts[2]) {
			continue
		}
		l := s.snapLock(parts[1], parts[2])
		l.Lock()
		// the listing is old: look again under the snapshot lock, which a commit holds while it writes
		if ok, err := datastore.Exists(ctx, s.b, dir+fileCommit); err != nil || ok {
			l.Unlock()
			continue
		}
		for _, o := range byDir[dir] {
			if info, err := s.b.Head(ctx, o.key); err != nil || info.ModTime.After(cutoff) {
				continue
			}
			if s.b.Delete(ctx, o.key) == nil {
				n++
			}
		}
		l.Unlock()
	}
	return n, nil
}

// ---- escrow ---------------------------------------------------------------------

// MaxEscrowBytes bounds an escrow blob.
const MaxEscrowBytes = 64 << 10

func escrowKey(domain string) string { return escrowDir + domain + escrowExt }

// PutEscrow stores the wrapped master key of a key domain, create-only: a
// compromised host cannot silently replace it.
func (s *Store) PutEscrow(ctx context.Context, domain string, blob []byte) error {
	if !format.ValidDomainID(domain) {
		return fmt.Errorf("%w: key domain", ErrInvalid)
	}
	if len(blob) == 0 || len(blob) > MaxEscrowBytes {
		return fmt.Errorf("%w: escrow blob has an implausible size", ErrInvalid)
	}
	err := datastore.PutBytes(ctx, s.b, escrowKey(domain), blob, datastore.PutOptions{IfAbsent: true})
	if errors.Is(err, datastore.ErrExists) {
		return ErrExists
	}
	return err
}

// GetEscrow returns the wrapped key of a domain (ErrNotFound if none).
func (s *Store) GetEscrow(ctx context.Context, domain string) ([]byte, error) {
	if !format.ValidDomainID(domain) {
		return nil, fmt.Errorf("%w: key domain", ErrInvalid)
	}
	b, err := datastore.ReadAll(ctx, s.b, escrowKey(domain), MaxEscrowBytes)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, ErrNotFound
	}
	return b, err
}

// DeleteEscrow removes the blob (administrator only).
func (s *Store) DeleteEscrow(ctx context.Context, domain string) error {
	if !format.ValidDomainID(domain) {
		return fmt.Errorf("%w: key domain", ErrInvalid)
	}
	return s.b.Delete(ctx, escrowKey(domain))
}

// ListEscrow returns the key domains that have an escrow blob.
func (s *Store) ListEscrow(ctx context.Context) ([]string, error) {
	var out []string
	err := s.b.List(ctx, escrowDir, func(o datastore.ObjectInfo) error {
		n := strings.TrimSuffix(strings.TrimPrefix(o.Key, escrowDir), escrowExt)
		if format.ValidDomainID(n) && escrowKey(n) == o.Key {
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

// HasEscrow reports whether an escrow blob exists for the key domain.
func (s *Store) HasEscrow(ctx context.Context, domain string) (bool, error) {
	if !format.ValidDomainID(domain) {
		return false, fmt.Errorf("%w: key domain", ErrInvalid)
	}
	return datastore.Exists(ctx, s.b, escrowKey(domain))
}

// ---- verification record ---------------------------------------------------------------

// VerifyRecord is verify.json, written by the node's verification and by hosts
// after a restore test. Failed is the only field the engine reads: a failed
// snapshot is never the base of an incremental run.
type VerifyRecord struct {
	Failed   bool      `json:"failed"`
	Verified time.Time `json:"verified,omitzero"`
	Mode     string    `json:"mode,omitempty"`
	Detail   string    `json:"detail,omitempty"`
}

// SetVerify records the verification result of a committed snapshot
// (verify.json is replaceable).
func (s *Store) SetVerify(ctx context.Context, group, snap string, r VerifyRecord) error {
	if err := checkIDs(group, snap); err != nil {
		return err
	}
	l := s.snapLock(group, snap)
	l.Lock()
	defer l.Unlock()
	if ok, err := datastore.Exists(ctx, s.b, SnapPrefix(group, snap)+fileCommit); err != nil {
		return err
	} else if !ok {
		return ErrNotFound
	}
	b, _ := json.Marshal(r)
	return datastore.PutBytes(ctx, s.b, SnapPrefix(group, snap)+fileVerify, b, datastore.PutOptions{})
}

// ReadVerify returns verify.json of a committed snapshot, or nil when none was recorded.
func (s *Store) ReadVerify(ctx context.Context, group, snap string) (*VerifyRecord, error) {
	b, err := s.readSnap(ctx, group, snap, fileVerify, maxReceiptBytes)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r VerifyRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("verify.json of %s/%s: %w", group, snap, err)
	}
	return &r, nil
}
