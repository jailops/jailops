package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"jailops.net/jailops/internal/repo/catalog"
	"jailops.net/jailops/internal/repo/format"
)

// Snapshot is a committed snapshot opened with the key: its verified index and
// manifest.
type Snapshot struct {
	Group    string
	ID       string
	Index    *format.Index
	IndexRaw []byte
	Manifest *format.Manifest
	Receipt  *format.Receipt

	keys *format.Keys
	repo Repository
}

// OpenSnapshot reads and verifies a committed snapshot ("" = the latest): the
// manifest must authenticate for this key, group and snapshot id, its index
// digest must match the index, the streams must tile the index and the receipt
// must agree with both files. A tampered repository is refused here.
func OpenSnapshot(ctx context.Context, repo Repository, keys *format.Keys, group, snapshot string) (*Snapshot, error) {
	if snapshot == "" {
		l, err := repo.ListSnapshots(ctx, group)
		if err != nil {
			return nil, err
		}
		if len(l) == 0 {
			return nil, ErrNoSnapshot
		}
		snapshot = l[len(l)-1].ID
	}
	ib, err := repo.ReadIndex(ctx, group, snapshot)
	if err != nil {
		return nil, err
	}
	mb, err := repo.ReadManifest(ctx, group, snapshot)
	if err != nil {
		return nil, err
	}
	rc, err := repo.ReadReceipt(ctx, group, snapshot)
	if err != nil {
		return nil, err
	}
	man, err := keys.OpenManifest(mb, group, snapshot)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWrongKey, err)
	}
	idx, err := format.ParseIndex(ib)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := man.CheckIndex(ib, idx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if rc.IndexDigest != format.FileDigest(ib) || rc.ManifestDigest != format.FileDigest(mb) {
		return nil, fmt.Errorf("%w: the commit receipt does not match the index and manifest", ErrCorrupt)
	}
	return &Snapshot{Group: group, ID: snapshot, Index: idx, IndexRaw: ib, Manifest: man, Receipt: rc, keys: keys, repo: repo}, nil
}

// Stream returns the manifest entry of a dataset stream.
func (s *Snapshot) Stream(name string) (*format.Stream, error) {
	for i := range s.Manifest.Streams {
		if s.Manifest.Streams[i].Name == name {
			return &s.Manifest.Streams[i], nil
		}
	}
	return nil, fmt.Errorf("snapshot %s has no stream %q", s.ID, name)
}

// payloadIndex and catalogIndex return the index ranges of a stream.
func (s *Snapshot) payloadIndex(st *format.Stream) (*format.Index, error) {
	return s.Index.Sub(st.Offset, st.Offset+st.PayloadSize)
}

func (s *Snapshot) catalogIndex(st *format.Stream) (*format.Index, error) {
	return s.Index.Sub(st.Offset+st.PayloadSize, st.Offset+st.PayloadSize+st.CatalogSize)
}

// LoadCatalog fetches only the catalog chunks of a stream and decodes them. The
// catalog is held in memory (about 400 bytes per entry).
func (s *Snapshot) LoadCatalog(ctx context.Context, st *format.Stream, workers, window int) (*catalog.Tree, error) {
	ci, err := s.catalogIndex(st)
	if err != nil {
		return nil, fmt.Errorf("%w: catalog range of %s: %v", ErrCorrupt, st.Name, err)
	}
	var buf bytes.Buffer
	f := newFetcher(s.keys, s.repo, workers, window)
	if err := f.stream(ctx, ci, func(_ int, data []byte) error { buf.Write(data); return nil }); err != nil {
		return nil, err
	}
	t, err := catalog.Decode(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("%w: catalog of %s: %v", ErrCorrupt, st.Name, err)
	}
	return t, nil
}

// base is what the previous snapshot offers for one stream.
type base struct {
	snap *Snapshot
	st   *format.Stream
	tree *catalog.Tree
	idx  *format.Index // the whole index of the previous snapshot
}

// chooseBase finds the snapshot to base the run on. A reason is returned when
// there is none; the run then reads every file.
func (r *run) chooseBase(ctx context.Context) (*Snapshot, string, error) {
	o := r.o
	if o.NoPrevious {
		return nil, "a full read was requested", nil
	}
	l, err := o.Repo.ListSnapshots(ctx, r.group)
	if err != nil {
		return nil, "", fmt.Errorf("listing snapshots: %w", err)
	}
	if len(l) == 0 {
		return nil, "there is no previous snapshot", nil
	}
	pick := l[len(l)-1]
	if o.Previous != "" {
		found := false
		for _, s := range l {
			if s.ID == o.Previous {
				pick, found = s, true
			}
		}
		if !found {
			return nil, "", fmt.Errorf("%w: previous snapshot %s", ErrNoSnapshot, o.Previous)
		}
	}
	if pick.Failed {
		return nil, fmt.Sprintf("the previous snapshot %s failed verification", pick.ID), nil
	}
	snap, err := OpenSnapshot(ctx, o.Repo, r.keys, r.group, pick.ID)
	if err != nil {
		if errors.Is(err, ErrWrongKey) {
			return nil, "", err
		}
		return nil, fmt.Sprintf("the previous snapshot %s cannot be used: %v", pick.ID, err), nil
	}
	return snap, "", nil
}

// streamBase loads the catalog of the named stream of the previous snapshot, or
// says why it cannot be used.
func (r *run) streamBase(ctx context.Context, snap *Snapshot, name string, created time.Time) (*base, string) {
	st, err := snap.Stream(name)
	if err != nil {
		return nil, "the previous snapshot has no such dataset"
	}
	if st.LastFull.IsZero() {
		return nil, "the previous snapshot does not record when everything was last read"
	}
	if created.Sub(st.LastFull) >= r.fullEvery || st.LastFull.After(created.Add(time.Minute)) {
		return nil, fmt.Sprintf("the last full read is older than %s (or dated in the future: the clock moved)", r.fullEvery)
	}
	if st.TrustCtime != !r.o.NoTrustCtime {
		return nil, "the ctime and inode comparison setting changed"
	}
	tree, err := snap.LoadCatalog(ctx, st, r.workers, r.window)
	if err != nil {
		return nil, "the previous catalog cannot be read: " + err.Error()
	}
	return &base{snap: snap, st: st, tree: tree, idx: snap.Index}, ""
}

// reuseSlots maps the old payload range of a reusable run onto whole chunks of
// the base index. ok is false when the old chunk boundaries are not there.
func (b *base) reuseSlots(run *catalog.Run) ([]slot, bool) {
	if run.OldEnd > b.st.PayloadSize || run.OldStart >= run.OldEnd {
		return nil, false
	}
	sub, err := b.idx.Sub(b.st.Offset+run.OldStart, b.st.Offset+run.OldEnd)
	if err != nil {
		return nil, false
	}
	out := make([]slot, 0, len(sub.Entries))
	var prev uint64
	for _, en := range sub.Entries {
		out = append(out, slot{size: en.End - prev, id: en.ID})
		prev = en.End
	}
	return out, true
}
