package replicate

import (
	"errors"
	"sync"
)

// Reverter runs undo functions in reverse order unless disarmed. Use
// `defer r.Revert()` right after creating it and call Success when the whole
// operation worked.
type Reverter struct {
	mu   sync.Mutex
	fns  []func() error
	done bool
	errs []error
}

// Add registers an undo step.
func (r *Reverter) Add(fn func() error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fns = append(r.fns, fn)
}

// Success disarms the reverter.
func (r *Reverter) Success() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done = true
	r.fns = nil
}

// Revert runs the undo steps, newest first, and returns their joined errors.
// It is safe to call more than once.
func (r *Reverter) Revert() error {
	r.mu.Lock()
	fns := r.fns
	r.fns = nil
	r.mu.Unlock()
	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](); err != nil {
			errs = append(errs, err)
		}
	}
	r.mu.Lock()
	r.errs = append(r.errs, errs...)
	all := errors.Join(r.errs...)
	r.mu.Unlock()
	return all
}
