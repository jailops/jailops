package engine

import (
	"context"
	"sync"
	"sync/atomic"

	"jailops.net/jailops/internal/repo/chunker"
	"jailops.net/jailops/internal/repo/format"
)

// result is the id of one chunk, filled in by a worker before the pipeline's
// wait returns.
type result struct{ id format.ID }

// slot is one index entry of a stream being built: a size and either a known id
// (reused, or zero) or a result still being computed.
type slot struct {
	size uint64
	id   format.ID
	res  *result
}

func (s *slot) resolve() format.ID {
	if s.res != nil {
		return s.res.id
	}
	return s.id
}

// counters collect what the workers did for one stream.
type counters struct {
	uploadedBytes atomic.Uint64
	newChunks     atomic.Int64
}

type job struct {
	data []byte
	res  *result
	ct   *counters
}

// pipeline turns plaintext chunks into uploaded objects with a bounded number of
// chunks in flight: the queue holds at most window chunks and workers hold at
// most one each.
type pipeline struct {
	keys *format.Keys
	up   Uploader

	jobs chan job
	wg   sync.WaitGroup

	errMu    sync.Mutex
	firstErr error
	cancel   context.CancelFunc

	// inflight deduplicates chunks that two workers hold at once.
	inflightMu sync.Mutex
	inflight   map[format.ID]chan struct{}
}

func newPipeline(ctx context.Context, cancel context.CancelFunc, keys *format.Keys, up Uploader, workers, window int) *pipeline {
	p := &pipeline{keys: keys, up: up, jobs: make(chan job, window), cancel: cancel, inflight: map[format.ID]chan struct{}{}}
	for i := 0; i < workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for j := range p.jobs {
				if ctx.Err() != nil {
					continue // drain
				}
				if err := p.work(ctx, j); err != nil {
					p.fail(err)
				}
			}
		}()
	}
	return p
}

func (p *pipeline) fail(err error) {
	p.errMu.Lock()
	if p.firstErr == nil {
		p.firstErr = err
		p.cancel()
	}
	p.errMu.Unlock()
}

func (p *pipeline) err() error {
	p.errMu.Lock()
	defer p.errMu.Unlock()
	return p.firstErr
}

func (p *pipeline) work(ctx context.Context, j job) error {
	id := p.keys.ChunkID(j.data)
	j.res.id = id
	// another worker may be handling the same chunk right now
	p.inflightMu.Lock()
	if ch, busy := p.inflight[id]; busy {
		p.inflightMu.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	done := make(chan struct{})
	p.inflight[id] = done
	p.inflightMu.Unlock()
	defer close(done)
	// a chunk the repository holds is never compressed or encrypted
	miss, err := p.up.Missing(ctx, []format.ID{id})
	if err != nil {
		return err
	}
	if len(miss) == 0 {
		return nil
	}
	blob, err := p.keys.SealChunk(id, j.data, format.EncodeOpts{})
	if err != nil {
		return err
	}
	if err := p.up.PutChunk(ctx, id, blob); err != nil {
		return err
	}
	j.ct.uploadedBytes.Add(uint64(len(blob)))
	j.ct.newChunks.Add(1)
	return nil
}

// emit hands one chunk to the workers, or returns a zero slot for an all-zero
// chunk. It blocks while the queue is full (the backpressure on the reader).
func (p *pipeline) emit(ctx context.Context, data []byte, ct *counters) (slot, error) {
	if chunker.IsZero(data) {
		return slot{size: uint64(len(data))}, nil
	}
	res := &result{}
	select {
	case p.jobs <- job{data: data, res: res, ct: ct}:
		return slot{size: uint64(len(data)), res: res}, nil
	case <-ctx.Done():
		if err := p.err(); err != nil {
			return slot{}, err
		}
		return slot{}, ctx.Err()
	}
}

// finish waits for every queued chunk and returns the first error.
func (p *pipeline) finish() error {
	close(p.jobs)
	p.wg.Wait()
	return p.err()
}
