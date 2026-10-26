package hostbackup

import (
	"context"
	"errors"
)

// reverter runs undo functions in reverse order. Whatever a multi-step
// operation created is registered before the step that creates it, so a
// failure at any step (including the step itself half-done) is cleaned up.
type reverter struct{ fns []func(context.Context) error }

func (r *reverter) add(fn func(context.Context) error) { r.fns = append(r.fns, fn) }

// revert runs the steps newest first on a context that outlives cancellation
// of the operation, and joins their errors. A second call does nothing.
func (r *reverter) revert(ctx context.Context) error {
	fns := r.fns
	r.fns = nil
	ctx = context.WithoutCancel(ctx)
	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](ctx); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// disarm drops the undo steps (the operation succeeded).
func (r *reverter) disarm() { r.fns = nil }
