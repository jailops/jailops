package capture

import (
	"fmt"
	"strconv"
	"strings"
)

// ZfsDiffOp is one line of `zfs diff -FHt <old> <new>`: tab-separated timestamp, change
// ('-' removed, '+' created, 'M' modified, 'R' renamed), file type, path, and for a rename a
// second path. UNVERIFIED: exact column order and type letters are from OpenZFS documentation
// memory, not a run; ParseZfsDiff reports a parse error rather than guessing, so a format
// mismatch is visible immediately.
type ZfsDiffOp struct {
	Time    float64
	Change  byte
	Type    byte
	Path    string
	NewPath string // set only when Change == 'R'
}

// ParseZfsDiff parses the output of `zfs diff -FHt`.
func ParseZfsDiff(out string) ([]ZfsDiffOp, error) {
	var ops []ZfsDiffOp
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l == "" {
			continue
		}
		f := strings.Split(l, "\t")
		if len(f) < 4 {
			return nil, fmt.Errorf("bad zfs diff line %q: want at least 4 tab-separated fields", l)
		}
		ts, err := strconv.ParseFloat(f[0], 64)
		if err != nil {
			return nil, fmt.Errorf("bad timestamp in %q: %w", l, err)
		}
		if len(f[1]) != 1 || len(f[2]) != 1 {
			return nil, fmt.Errorf("bad change/type field in %q", l)
		}
		op := ZfsDiffOp{Time: ts, Change: f[1][0], Type: f[2][0], Path: f[3]}
		if op.Change == 'R' {
			if len(f) < 5 || f[4] == "" {
				return nil, fmt.Errorf("rename line missing new path: %q", l)
			}
			op.NewPath = f[4]
		}
		switch op.Change {
		case '-', '+', 'M', 'R':
		default:
			return nil, fmt.Errorf("unknown change code %q in %q", string(op.Change), l)
		}
		ops = append(ops, op)
	}
	return ops, nil
}

// ZfsDiffChangeSet turns parsed zfs diff output into the same shape Compare produces, so it can
// be checked with Equal against a full L0 walk (design 9.4 step 4's fuzz test, and section 16
// row 10). A rename counts as removed(old)+added(new): whether FreeBSD's zfs diff instead
// reports a rename of an otherwise-identical file as an 'M' or leaves it out entirely is exactly
// what this comparison is meant to catch.
func ZfsDiffChangeSet(ops []ZfsDiffOp) ChangeSet { return ZfsDiffChangeSetRel(ops, "") }

// ZfsDiffChangeSetRel is ZfsDiffChangeSet with paths made relative to base, the mountpoint of the
// dataset: zfs diff prints absolute paths, a tree walk produces paths relative to the dataset root
// ("." for the root itself). Verified on FreeBSD 15.1: without this the two never compare equal.
func ZfsDiffChangeSetRel(ops []ZfsDiffOp, base string) ChangeSet {
	rel := func(p string) string {
		if base == "" {
			return p
		}
		b := strings.TrimSuffix(base, "/")
		if p == b || p == b+"/" {
			return "."
		}
		return strings.TrimPrefix(p, b+"/")
	}
	var cs ChangeSet
	for _, o := range ops {
		o.Path, o.NewPath = rel(o.Path), rel(o.NewPath)
		switch o.Change {
		case '-':
			cs.Removed = append(cs.Removed, o.Path)
		case '+':
			cs.Added = append(cs.Added, o.Path)
		case 'M':
			cs.Modified = append(cs.Modified, o.Path)
		case 'R':
			cs.Removed = append(cs.Removed, o.Path)
			cs.Added = append(cs.Added, o.NewPath)
		}
	}
	cs.sort()
	return cs
}
