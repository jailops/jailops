package node

import (
	"context"
	"encoding/binary"
	"sync"
	"sync/atomic"

	"jailops.net/jailops/internal/repo/format"
)

// liveKey is the key of a chunk id in the GC live set and in session pins: its
// first 8 bytes. A collision can only make a garbage chunk look live and stay,
// never make a live chunk look dead.
func liveKey(id format.ID) uint64 { return binary.LittleEndian.Uint64(id[:8]) }

// forEach runs fn(i) for 0 <= i < n on at most par goroutines. It stops handing
// out work after the first error or when ctx ends, and returns the first error.
func forEach(ctx context.Context, par, n int, fn func(i int) error) error {
	if n == 0 {
		return nil
	}
	par = max(1, min(par, n))
	var wg sync.WaitGroup
	var next atomic.Int64
	var first atomic.Value
	for range par {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				if ctx.Err() != nil || first.Load() != nil {
					return
				}
				i := int(next.Add(1)) - 1
				if i >= n {
					return
				}
				if err := fn(i); err != nil {
					first.CompareAndSwap(nil, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if e := first.Load(); e != nil {
		return e.(error)
	}
	return ctx.Err()
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
