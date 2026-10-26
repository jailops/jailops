package backup

import "errors"

// reverter runs undo functions in reverse order. A drill never disarms it:
// whatever was created is removed, whether the drill passed or not.
type reverter struct{ fns []func() error }

// add registers an undo step.
func (r *reverter) add(fn func() error) { r.fns = append(r.fns, fn) }

// revert runs the undo steps, newest first, and returns their joined errors.
// Steps run once; a second call does nothing.
func (r *reverter) revert() error {
	fns := r.fns
	r.fns = nil
	var errs []error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
