package sdk

import (
	"context"
	"fmt"
	"iter"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"jailops.net/jailops/sdk/api"
)

// None is the result type of operations that return nothing.
type None struct{}

// Result is the final outcome of an operation: the typed value and the
// operation record.
type Result[T any] struct {
	Value     T
	Operation api.Operation
}

// Operation is a handle on an asynchronous server task whose successful
// result decodes as T. A server that answers a request synchronously yields
// an Operation that is already complete, so callers need not care.
type Operation[T any] struct {
	c     *Client
	fetch func(context.Context) (T, error)
	mu    sync.Mutex
	op    api.Operation
}

// ID is the operation id, empty when the server answered synchronously.
func (o *Operation[T]) ID() string { return o.Info().ID }

// Info returns the last known operation record.
func (o *Operation[T]) Info() api.Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.op
}

func (o *Operation[T]) set(op api.Operation) {
	o.mu.Lock()
	o.op = op
	o.mu.Unlock()
}

// Refresh reloads the operation record.
func (o *Operation[T]) Refresh(ctx context.Context) error {
	id := o.ID()
	if id == "" {
		return nil
	}
	op, err := o.c.Operations().Get(ctx, id)
	if err == nil {
		o.set(op)
	}
	return err
}

// Cancel asks the server to cancel the operation.
func (o *Operation[T]) Cancel(ctx context.Context) error {
	if o.ID() == "" {
		return nil
	}
	return o.c.Operations().Cancel(ctx, o.ID())
}

// Wait blocks until the operation ends and returns its typed result. A failed
// operation returns its *api.Error, a cancelled one ErrCancelled.
func (o *Operation[T]) Wait(ctx context.Context) (T, error) {
	r, err := o.WaitResult(ctx)
	return r.Value, err
}

// WaitResult is Wait that also returns the operation record.
func (o *Operation[T]) WaitResult(ctx context.Context) (Result[T], error) {
	var res Result[T]
	cur := o.Info()
	if !cur.Status.Final() {
		var err error
		cur, err = waitFinal(ctx, o.c, cur.ID)
		if err != nil {
			res.Operation = o.Info()
			return res, err
		}
		o.set(cur)
	}
	res.Operation = cur
	if err := operationError(cur); err != nil {
		return res, err
	}
	v, err := decodeMeta[T](cur.Metadata)
	if err != nil {
		return res, err
	}
	if len(cur.Metadata) == 0 && o.fetch != nil {
		if v, err = o.fetch(ctx); err != nil {
			return res, err
		}
	}
	res.Value = v
	return res, nil
}

func operationError(op api.Operation) error {
	switch op.Status {
	case api.OpFailure:
		if op.Error != nil {
			return op.Error
		}
		return api.Errorf(api.CodeInternal, "operation %s failed", op.ID)
	case api.OpCancelled:
		return fmt.Errorf("%w: %s", ErrCancelled, op.ID)
	}
	return nil
}

const waitChunk = 25 * time.Second

// waitFinal long-polls /wait and falls back to polling when the server has no
// working wait endpoint.
func waitFinal(ctx context.Context, c *Client, id string) (api.Operation, error) {
	poll := false
	interval := 100 * time.Millisecond
	for {
		var op api.Operation
		var err error
		start := time.Now()
		if !poll {
			q := url.Values{"timeout": {strconv.Itoa(int(waitChunk / time.Second))}}
			op, _, err = send[api.Operation](ctx, c, request{
				method: http.MethodGet, path: "/api/v1/operations/" + esc(id) + "/wait",
				query: q, timeout: waitChunk + 15*time.Second,
			})
			if err != nil && (api.IsCode(err, api.CodeNotFound) || IsUnsupported(err)) {
				poll = true
				continue
			}
			if err == nil && !op.Status.Final() && time.Since(start) < time.Second {
				poll = true // the server ignores the timeout; do not spin
			}
		} else {
			op, err = c.Operations().Get(ctx, id)
		}
		if err != nil {
			return op, err
		}
		if op.Status.Final() {
			return op, nil
		}
		if poll {
			d := interval/2 + rand.N(interval/2+1)
			select {
			case <-time.After(d):
			case <-ctx.Done():
				return op, ctx.Err()
			case <-c.t.ctx.Done():
				return op, ErrClosed
			}
			interval = min(interval*2, 2*time.Second)
		}
	}
}

// OperationClient accesses /api/v1/operations.
type OperationClient struct{ c *Client }

// Operations returns the operations client.
func (c *Client) Operations() *OperationClient { return &OperationClient{c} }

// Get fetches one operation.
func (o *OperationClient) Get(ctx context.Context, id string) (api.Operation, error) {
	op, _, err := get[api.Operation](ctx, o.c, "/api/v1/operations/"+esc(id), nil)
	return op, err
}

// List iterates operations.
func (o *OperationClient) List(ctx context.Context, opts ...ListOption) iter.Seq2[api.Operation, error] {
	return list[api.Operation](ctx, o.c, "/api/v1/operations", opts)
}

// Wait waits for the operation to end. The record is returned even when the
// error reports a failed or cancelled operation.
func (o *OperationClient) Wait(ctx context.Context, id string) (api.Operation, error) {
	op, err := waitFinal(ctx, o.c, id)
	if err != nil {
		return op, err
	}
	return op, operationError(op)
}

// Cancel requests cancellation.
func (o *OperationClient) Cancel(ctx context.Context, id string) error {
	_, _, err := send[None](ctx, o.c, request{method: http.MethodDelete, path: "/api/v1/operations/" + esc(id)})
	return err
}
