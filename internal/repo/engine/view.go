package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"sort"
	"sync"

	"jailops.net/jailops/internal/repo/catalog"
	"jailops.net/jailops/internal/repo/format"
)

const viewCacheChunks = 8

// View is an opened dataset stream of a snapshot: its catalog, and random
// access to its payload that fetches only the chunks a request needs. Single
// file and subtree restore and browsing use it.
type View struct {
	Snap   *Snapshot
	Stream *format.Stream
	Tree   *catalog.Tree

	ctx    context.Context
	f      *fetcher
	payIdx *format.Index

	mu    sync.Mutex
	cache map[int][]byte
	order []int
}

// OpenView loads the catalog of the named stream (only the catalog chunks are
// fetched). ctx must outlive the view: later reads use it.
func (s *Snapshot) OpenView(ctx context.Context, stream string, workers, window int) (*View, error) {
	st, err := s.Stream(stream)
	if err != nil {
		return nil, err
	}
	tree, err := s.LoadCatalog(ctx, st, workers, window)
	if err != nil {
		return nil, err
	}
	pi, err := s.payloadIndex(st)
	if err != nil {
		return nil, fmt.Errorf("%w: payload range of %s: %v", ErrCorrupt, stream, err)
	}
	if err := checkCatalogRanges(tree, st.PayloadSize); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return &View{Snap: s, Stream: st, Tree: tree, ctx: ctx, f: newFetcher(s.keys, s.repo, workers, window), payIdx: pi, cache: map[int][]byte{}}, nil
}

// checkCatalogRanges verifies that every file's payload range lies inside the
// payload, so a damaged catalog cannot make a reader run off the stream.
func checkCatalogRanges(t *catalog.Tree, payload uint64) error {
	var bad error
	t.Walk(func(p string, e *catalog.Entry) bool {
		if e.Kind == catalog.KFile && e.Size > 0 && (e.PayOff > payload || e.Size > payload-e.PayOff) {
			bad = fmt.Errorf("catalog entry %s points outside the payload", p)
			return false
		}
		return bad == nil
	})
	return bad
}

func (v *View) chunk(i int) ([]byte, error) {
	v.mu.Lock()
	if d, ok := v.cache[i]; ok {
		v.mu.Unlock()
		return d, nil
	}
	v.mu.Unlock()
	ent := v.payIdx.Entries[i]
	_, size := v.payIdx.Span(i)
	var data []byte
	if ent.ID.IsZero() {
		data = zeros(size)
	} else {
		var err error
		if data, err = v.f.chunk(v.ctx, ent.ID, size); err != nil {
			return nil, err
		}
	}
	v.mu.Lock()
	v.cache[i] = data
	v.order = append(v.order, i)
	for len(v.order) > viewCacheChunks {
		delete(v.cache, v.order[0])
		v.order = v.order[1:]
	}
	v.mu.Unlock()
	return data, nil
}

// CachedChunks lists the payload chunks currently held in memory (tests).
func (v *View) CachedChunks() []int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]int(nil), v.order...)
}

// ReadAt implements io.ReaderAt over the payload.
func (v *View) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || uint64(off) >= v.Stream.PayloadSize {
		return 0, io.EOF
	}
	i := sort.Search(len(v.payIdx.Entries), func(i int) bool { return v.payIdx.Entries[i].End > uint64(off) })
	n := 0
	for n < len(p) && i < len(v.payIdx.Entries) {
		start, _ := v.payIdx.Span(i)
		d, err := v.chunk(i)
		if err != nil {
			return n, err
		}
		n += copy(p[n:], d[uint64(off)+uint64(n)-start:])
		i++
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// Extract writes the selected paths (all when empty) below dest. Only the
// chunks that hold the requested files' bytes are fetched.
func (v *View) Extract(dest string, o catalog.ExtractOptions) error {
	return v.Tree.Extract(dest, catalog.FromReaderAt(v), o)
}

// ExtractTo is Extract into any sink.
func (v *View) ExtractTo(sink catalog.Sink, o catalog.ExtractOptions) error {
	return v.Tree.ExtractTo(sink, catalog.FromReaderAt(v), o)
}

// Cat copies one regular file to w, verifying its SHA-256.
func (v *View) Cat(w io.Writer, path string) error {
	e, err := v.Tree.Lookup(path)
	if err != nil {
		return err
	}
	if e.Kind == catalog.KHardlink {
		if e, err = v.Tree.Lookup(e.Link); err != nil {
			return err
		}
	}
	if e.Kind != catalog.KFile {
		return fmt.Errorf("%s is a %s, not a regular file", path, e.Kind)
	}
	if e.Size == 0 {
		return nil
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(w, h), io.NewSectionReader(v, int64(e.PayOff), int64(e.Size))); err != nil {
		return err
	}
	var got [32]byte
	copy(got[:], h.Sum(nil))
	if got != e.Sum {
		return fmt.Errorf("%w: %s does not match its recorded SHA-256", ErrCorrupt, path)
	}
	return nil
}
