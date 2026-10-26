package hostbackup

import (
	"context"
	"sync"
	"time"

	"jailops.net/jailops/internal/node/wire"
	"jailops.net/jailops/internal/repo/engine"
	"jailops.net/jailops/internal/repo/format"
)

// batchDelay is how long a single-id Missing query waits for company.
const batchDelay = 3 * time.Millisecond

// batchUploader wraps an engine.Uploader and turns the one-id Missing queries
// of the engine's parallel workers into batched requests (up to
// wire.MaxMissingBatch ids per call). Larger queries are split. Ids found
// present are remembered (the session pins them), so each id is asked once.
type batchUploader struct {
	engine.Uploader
	ctx context.Context // the run's context, for the flush

	mu      sync.Mutex
	queue   []*waiter
	timer   *time.Timer
	present map[format.ID]struct{}

	// Calls and Batched count the requests sent and the ids in them (tests).
	calls, asked int
}

type waiter struct {
	id format.ID
	ch chan waitResult
}

type waitResult struct {
	missing bool
	err     error
}

func newBatchUploader(ctx context.Context, u engine.Uploader) *batchUploader {
	return &batchUploader{Uploader: u, ctx: ctx, present: map[format.ID]struct{}{}}
}

// Missing implements engine.Uploader.
func (b *batchUploader) Missing(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	if len(ids) == 1 {
		return b.single(ctx, ids[0])
	}
	var out []format.ID
	for len(ids) > 0 {
		n := min(len(ids), wire.MaxMissingBatch)
		m, err := b.ask(ctx, ids[:n])
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
		ids = ids[n:]
	}
	return out, nil
}

func (b *batchUploader) ask(ctx context.Context, ids []format.ID) ([]format.ID, error) {
	b.mu.Lock()
	b.calls++
	b.asked += len(ids)
	b.mu.Unlock()
	m, err := b.Uploader.Missing(ctx, ids)
	if err != nil {
		return nil, err
	}
	gone := make(map[format.ID]bool, len(m))
	for _, id := range m {
		gone[id] = true
	}
	b.mu.Lock()
	for _, id := range ids {
		if !gone[id] {
			b.present[id] = struct{}{}
		}
	}
	b.mu.Unlock()
	return m, nil
}

func (b *batchUploader) single(ctx context.Context, id format.ID) ([]format.ID, error) {
	b.mu.Lock()
	if _, ok := b.present[id]; ok {
		b.mu.Unlock()
		return nil, nil
	}
	w := &waiter{id: id, ch: make(chan waitResult, 1)}
	b.queue = append(b.queue, w)
	switch {
	case len(b.queue) >= wire.MaxMissingBatch:
		q := b.takeLocked()
		b.mu.Unlock()
		b.flush(q)
	case b.timer == nil:
		b.timer = time.AfterFunc(batchDelay, func() {
			b.mu.Lock()
			q := b.takeLocked()
			b.mu.Unlock()
			b.flush(q)
		})
		b.mu.Unlock()
	default:
		b.mu.Unlock()
	}
	select {
	case r := <-w.ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.missing {
			return []format.ID{id}, nil
		}
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *batchUploader) takeLocked() []*waiter {
	q := b.queue
	b.queue = nil
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	return q
}

func (b *batchUploader) flush(q []*waiter) {
	if len(q) == 0 {
		return
	}
	seen := map[format.ID]struct{}{}
	var ids []format.ID
	for _, w := range q {
		if _, dup := seen[w.id]; !dup {
			seen[w.id] = struct{}{}
			ids = append(ids, w.id)
		}
	}
	m, err := b.ask(b.ctx, ids)
	gone := make(map[format.ID]bool, len(m))
	for _, id := range m {
		gone[id] = true
	}
	for _, w := range q {
		w.ch <- waitResult{missing: gone[w.id], err: err}
	}
}

func (b *batchUploader) stats() (calls, asked int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.asked
}
