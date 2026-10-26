package engine

import (
	"context"
	"fmt"
	"sync"

	"jailops.net/jailops/internal/repo/format"
)

// zeroChunk backs every all-zero range handed to a callback: callbacks only read
// it, so a sparse file costs no allocation. It lives in BSS: untouched pages are
// never resident.
var zeroChunk [format.MaxChunkSize]byte

func zeros(n uint64) []byte {
	if n <= uint64(len(zeroChunk)) {
		return zeroChunk[:n:n]
	}
	return make([]byte, n)
}

// fetcher reads and decodes chunks of an index.
type fetcher struct {
	keys    *format.Keys
	repo    Repository
	workers int
	window  int
}

func newFetcher(keys *format.Keys, repo Repository, workers, window int) *fetcher {
	if workers <= 0 {
		workers = DefaultWorkers
	}
	if window <= 0 {
		window = DefaultWindow
	}
	return &fetcher{keys: keys, repo: repo, workers: workers, window: window}
}

// chunk fetches one chunk, decrypts it and recomputes its id.
func (f *fetcher) chunk(ctx context.Context, id format.ID, size uint64) ([]byte, error) {
	blob, err := f.repo.GetChunk(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("fetching chunk %s: %w", id, err)
	}
	pt, err := f.keys.DecodeChunk(blob, id)
	if err != nil {
		return nil, fmt.Errorf("%w: chunk %s: %v", ErrCorrupt, id, err)
	}
	if uint64(len(pt)) != size {
		return nil, fmt.Errorf("%w: chunk %s holds %d bytes, the index says %d", ErrCorrupt, id, len(pt), size)
	}
	return pt, nil
}

type fetched struct {
	data []byte
	err  error
}

// stream calls fn with the decoded chunks of idx in order. A window of chunks is
// fetched ahead by up to f.workers concurrent fetches, so memory depends on the
// window, not on the size of the object.
func (f *fetcher) stream(ctx context.Context, idx *format.Index, fn func(i int, data []byte) error) error {
	n := len(idx.Entries)
	futures := make([]chan fetched, n)
	sem := make(chan struct{}, f.workers)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	defer wg.Wait()
	next := 0
	launch := func(upTo int) {
		for ; next < upTo && next < n; next++ {
			i := next
			ent := idx.Entries[i]
			_, size := idx.Span(i)
			ch := make(chan fetched, 1)
			futures[i] = ch
			if ent.ID.IsZero() {
				ch <- fetched{}
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				select {
				case sem <- struct{}{}:
				case <-cctx.Done():
					ch <- fetched{err: cctx.Err()}
					return
				}
				defer func() { <-sem }()
				data, err := f.chunk(cctx, ent.ID, size)
				ch <- fetched{data: data, err: err}
			}()
		}
	}
	for i := 0; i < n; i++ {
		launch(i + f.window)
		r := <-futures[i]
		futures[i] = nil // a consumed slot holds nothing: the window, not the index, bounds memory
		if r.err != nil {
			cancel()
			return r.err
		}
		data := r.data
		if data == nil {
			_, size := idx.Span(i)
			data = zeros(size)
		}
		if err := fn(i, data); err != nil {
			cancel()
			return err
		}
	}
	return nil
}
