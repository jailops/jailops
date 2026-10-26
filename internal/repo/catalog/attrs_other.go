//go:build !linux && !freebsd

package catalog

// DefaultAttrs returns the extended attribute implementation of this platform:
// none.
func DefaultAttrs() Attrs { return noAttrs{} }
