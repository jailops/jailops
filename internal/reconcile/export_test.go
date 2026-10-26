package reconcile

import "context"

// LoadAnchorForTest exposes loadAnchor to the tests of this directory.
func (r *Reconciler) LoadAnchorForTest(ctx context.Context, name, ruleset string) error {
	return r.loadAnchor(ctx, name, ruleset)
}
