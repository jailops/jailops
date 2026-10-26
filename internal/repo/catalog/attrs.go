package catalog

import (
	"errors"
	"sort"
)

// Attrs collects and applies the platform metadata that is not part of stat(2):
// extended attributes, access control lists and similar. Each piece is an
// opaque named blob (Attr); the implementation chooses the names and is the only
// code that interprets them.
//
// Names are "<scheme>:<namespace>:<name>" so that a catalog written on one
// platform says exactly what it holds and a restore on another platform can
// refuse what it cannot apply instead of guessing:
//
//	linux:xattr:<full attribute name>        Linux extended attribute, including
//	                                         system.posix_acl_* and security.*
//	freebsd:extattr:user:<name>              FreeBSD user-namespace extended attribute
//	freebsd:extattr:system:<name>            FreeBSD system-namespace extended attribute
//	freebsd:acl:nfs4                         NFSv4 ACL, the kernel's struct acl
//
// Collect never follows symlinks.
type Attrs interface {
	// Name identifies the implementation ("linux", "freebsd", "none").
	Name() string
	// Collect returns every attribute of path, sorted by name.
	Collect(path string) ([]Attr, error)
	// Apply sets attrs on path (never following a symlink). Attributes the
	// platform cannot represent, or that the kernel refuses in a way the
	// options tolerate, are returned in skipped with a reason; an error is a
	// failure that must stop the restore.
	Apply(path string, attrs []Attr, o ApplyOptions) (skipped []SkippedAttr, err error)
}

// ApplyOptions tell Attrs.Apply which refusals are tolerable.
type ApplyOptions struct {
	// NoChown: ownership is not being restored, so privileged attributes
	// (security.*, trusted.*, system namespace ACLs of other owners) are not
	// applied: they would land on a file the extracting user owns.
	NoChown bool
	// Lenient tolerates EPERM, EINVAL and ENOTSUP from the kernel.
	Lenient bool
}

// SkippedAttr is an attribute that was not applied, and why.
type SkippedAttr struct {
	Name   string
	Reason string
}

// noAttrs is the implementation for platforms without extended attributes.
type noAttrs struct{}

func (noAttrs) Name() string                   { return "none" }
func (noAttrs) Collect(string) ([]Attr, error) { return nil, nil }
func (noAttrs) Apply(_ string, a []Attr, _ ApplyOptions) ([]SkippedAttr, error) {
	var out []SkippedAttr
	for _, x := range a {
		out = append(out, SkippedAttr{x.Name, "this platform has no extended attributes"})
	}
	return out, nil
}

// NoAttrs returns an Attrs that collects nothing and applies nothing (every
// attribute is reported as skipped).
func NoAttrs() Attrs { return noAttrs{} }

// ErrUnsupportedAttr is wrapped by Apply for an attribute whose scheme belongs
// to another platform.
var ErrUnsupportedAttr = errors.New("attribute belongs to another platform")

func sortAttrs(a []Attr) {
	sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name })
}
