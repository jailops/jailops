// Package capture holds the pure, Linux-testable logic of prototype 9: the L0 stat-walk
// comparison, `zfs diff -FHt` parsing, the randomized operation generator used to fuzz both
// against each other, and the deterministic digest used to verify a round trip. Nothing here
// touches a filesystem or runs a command; the walker that reads real files (stat, extattr,
// getfacl, chflags) lives in cmd/p9-capture, where it can be FreeBSD-specific. Nothing here was
// run on FreeBSD by its author.
package capture

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Entry is one filesystem object's metadata: every field design 9.4 names for L0 change
// detection (kind, mode, uid, gid, size, mtime_ns, ctime_ns, inode, link, xattr hash) plus ACL
// text and file flags, which design 9.4's FreeBSD-specifics paragraph also names.
type Entry struct {
	Kind       byte // 'f' regular, 'd' dir, 'l' symlink, 'p' fifo, 's' socket, 'b'/'c' device, '?' other
	Mode       uint32
	Uid, Gid   uint32
	Size       int64
	MtimeNs    int64
	CtimeNs    int64
	Inode      uint64
	Nlink      uint64
	LinkTarget string // symlink target; empty otherwise
	XattrHash  string // sha256 of sorted "namespace.name=value" pairs, "" if none
	Acl        string // getfacl-style text with path/owner/group/comment lines stripped, "" if trivial
	Flags      string // comma-joined chflags names, "" if none
}

// Tree maps a path (relative to the walked root, so two different roots compare directly) to
// its Entry.
type Tree map[string]Entry

// FieldDiff names one field that differs between two entries of the same path.
type FieldDiff string

// The fields L0 compares, named individually so a disagreement says exactly what moved.
const (
	DiffKind  FieldDiff = "kind"
	DiffMode  FieldDiff = "mode"
	DiffUid   FieldDiff = "uid"
	DiffGid   FieldDiff = "gid"
	DiffSize  FieldDiff = "size"
	DiffMtime FieldDiff = "mtime"
	DiffCtime FieldDiff = "ctime"
	DiffInode FieldDiff = "inode"
	DiffLink  FieldDiff = "link"
	DiffXattr FieldDiff = "xattr"
	DiffAcl   FieldDiff = "acl"
	DiffFlags FieldDiff = "flags"
)

// EntryDiff lists every field where b differs from a (same path, two points in time).
func EntryDiff(a, b Entry) []FieldDiff {
	var d []FieldDiff
	add := func(cond bool, f FieldDiff) {
		if cond {
			d = append(d, f)
		}
	}
	add(a.Kind != b.Kind, DiffKind)
	add(a.Mode != b.Mode, DiffMode)
	add(a.Uid != b.Uid, DiffUid)
	add(a.Gid != b.Gid, DiffGid)
	add(a.Size != b.Size, DiffSize)
	add(a.MtimeNs != b.MtimeNs, DiffMtime)
	add(a.CtimeNs != b.CtimeNs, DiffCtime)
	add(a.Inode != b.Inode, DiffInode)
	add(a.LinkTarget != b.LinkTarget, DiffLink)
	add(a.XattrHash != b.XattrHash, DiffXattr)
	add(a.Acl != b.Acl, DiffAcl)
	add(a.Flags != b.Flags, DiffFlags)
	return d
}

// Stable reports whether a path's inode and ctime are unchanged: the specific claim design
// 9.4 / section 16 row 9 needs for L0 to trust an unchanged file across snapshots without
// re-reading it.
func Stable(prev, cur Entry) bool { return prev.Inode == cur.Inode && prev.CtimeNs == cur.CtimeNs }

// ChangeSet is what L0 and the `zfs diff` accelerator must agree on: which paths were added,
// removed, or modified between two trees (a rename is removed(old) + added(new); see
// ZfsDiffChangeSet).
type ChangeSet struct {
	Added, Removed, Modified []string
}

// Compare walks both trees and builds the change set a full L0 walk would report.
func Compare(prev, cur Tree) ChangeSet {
	var cs ChangeSet
	for p, pe := range prev {
		ce, ok := cur[p]
		if !ok {
			cs.Removed = append(cs.Removed, p)
			continue
		}
		if len(EntryDiff(pe, ce)) > 0 {
			cs.Modified = append(cs.Modified, p)
		}
	}
	for p := range cur {
		if _, ok := prev[p]; !ok {
			cs.Added = append(cs.Added, p)
		}
	}
	cs.sort()
	return cs
}

func (cs *ChangeSet) sort() {
	sort.Strings(cs.Added)
	sort.Strings(cs.Removed)
	sort.Strings(cs.Modified)
}

// Equal reports whether two change sets name the same paths in the same categories, order and
// duplicates aside. Used to fuzz-test the zfs diff accelerator against a full L0 walk (design
// 9.4 step 4: "A fuzz test requires that the diff-driven result equals a full walk").
func (cs ChangeSet) Equal(other ChangeSet) bool {
	return sameSet(cs.Added, other.Added) && sameSet(cs.Removed, other.Removed) && sameSet(cs.Modified, other.Modified)
}

// Disagreements describes, in plain text, every category where cs and other differ; empty
// means Equal.
func (cs ChangeSet) Disagreements(other ChangeSet) []string {
	var out []string
	check := func(name string, a, b []string) {
		onlyA, onlyB := setDiff(a, b), setDiff(b, a)
		if len(onlyA) > 0 || len(onlyB) > 0 {
			out = append(out, fmt.Sprintf("%s: only in first=%v only in second=%v", name, onlyA, onlyB))
		}
	}
	check("added", cs.Added, other.Added)
	check("removed", cs.Removed, other.Removed)
	check("modified", cs.Modified, other.Modified)
	return out
}

func sameSet(a, b []string) bool { return len(setDiff(a, b)) == 0 && len(setDiff(b, a)) == 0 }

func setDiff(a, b []string) []string {
	bm := map[string]bool{}
	for _, x := range b {
		bm[x] = true
	}
	var out []string
	for _, x := range a {
		if !bm[x] {
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

// Digest returns a deterministic hex digest over every path's metadata (not raw content bytes;
// a round-trip test compares content separately, e.g. by also digesting file bytes). It
// excludes ctime and inode, which a restore cannot reproduce by design, and so is exactly the
// metadata a round trip is expected to preserve.
func Digest(t Tree) string {
	paths := make([]string, 0, len(t))
	for p := range t {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		e := t[p]
		fmt.Fprintf(h, "%s\x00%c\x00%o\x00%d\x00%d\x00%d\x00%d\x00%s\x00%s\x00%s\x00%s\n",
			p, e.Kind, e.Mode&0o7777, e.Uid, e.Gid, e.Size, e.MtimeNs, e.LinkTarget, e.XattrHash, e.Acl, e.Flags)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// XattrHash hashes a sorted set of "namespace.name=value" strings, the representation a
// walker builds after listing a file's extended attributes.
func XattrHash(pairs []string) string {
	if len(pairs) == 0 {
		return ""
	}
	cp := append([]string(nil), pairs...)
	sort.Strings(cp)
	h := sha256.New()
	h.Write([]byte(strings.Join(cp, "\x00")))
	return hex.EncodeToString(h.Sum(nil))
}
