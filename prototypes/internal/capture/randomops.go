package capture

import (
	"fmt"
	"math/rand"
	"sort"
)

// OpKind is one of the operation types design 9.4's equivalence test (section 16 row 10) names:
// create, modify, truncate, rename, hard link, delete, chmod, chown, xattr set, ACL set, flag
// set, directory rename.
type OpKind string

const (
	OpCreate    OpKind = "create"
	OpCreateDir OpKind = "create_dir"
	OpModify    OpKind = "modify"
	OpTruncate  OpKind = "truncate"
	OpRename    OpKind = "rename"
	OpHardlink  OpKind = "hardlink"
	OpDelete    OpKind = "delete"
	OpChmod     OpKind = "chmod"
	OpChown     OpKind = "chown"
	OpXattr     OpKind = "xattr"
	OpAcl       OpKind = "acl"
	OpFlag      OpKind = "flag"
)

// Op is one generated operation; Path (and NewPath, for rename/hardlink) are relative paths
// within the scratch tree. Arg carries the operation's parameter as text (new mode, new owner,
// xattr name=value, ACL entry, flag name), so the same Op can be logged, replayed and reported
// without a second representation.
type Op struct {
	Kind    OpKind
	Path    string
	NewPath string
	Arg     string
}

// FlagNames is the small, reversible subset of chflags(8) names GenOps uses: it avoids schg and
// any other flag that would block the operations that follow it.
var FlagNames = []string{"nodump", "uappnd", "uarchive"}

// GenOps deterministically generates n operations over a growing/shrinking set of paths, given
// seed. The same seed always produces the same sequence, which is what makes the fuzz
// comparison (L0 walk vs zfs diff) reproducible and a disagreement replayable by hand.
func GenOps(seed int64, n int) []Op {
	r := rand.New(rand.NewSource(seed))
	kinds := []OpKind{OpCreate, OpCreateDir, OpModify, OpTruncate, OpRename, OpHardlink, OpDelete,
		OpChmod, OpChown, OpXattr, OpAcl, OpFlag}
	alive := map[string]bool{}
	next := 0
	newName := func(prefix string) string {
		next++
		return fmt.Sprintf("%s%03d_%s", prefix, next, longSuffix(r))
	}
	pick := func() (string, bool) {
		if len(alive) == 0 {
			return "", false
		}
		names := make([]string, 0, len(alive))
		for p := range alive {
			names = append(names, p)
		}
		sort.Strings(names)
		return names[r.Intn(len(names))], true
	}

	var ops []Op
	for i := 0; i < n; i++ {
		switch k := kinds[r.Intn(len(kinds))]; k {
		case OpCreate:
			p := newName("rf")
			ops = append(ops, Op{Kind: OpCreate, Path: p})
			alive[p] = true
		case OpCreateDir:
			p := newName("rd")
			ops = append(ops, Op{Kind: OpCreateDir, Path: p})
			alive[p] = true
		case OpRename, OpHardlink:
			p, ok := pick()
			if !ok {
				continue
			}
			np := newName("rf")
			ops = append(ops, Op{Kind: k, Path: p, NewPath: np})
			if k == OpRename {
				delete(alive, p)
			}
			alive[np] = true
		case OpDelete:
			p, ok := pick()
			if !ok {
				continue
			}
			ops = append(ops, Op{Kind: OpDelete, Path: p})
			delete(alive, p)
		case OpChmod:
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: OpChmod, Path: p, Arg: fmt.Sprintf("%04o", r.Intn(0o777)+1)})
			}
		case OpChown:
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: OpChown, Path: p, Arg: fmt.Sprint(1000 + r.Intn(5))})
			}
		case OpXattr:
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: OpXattr, Path: p, Arg: fmt.Sprintf("jop9tag%d=v%d", r.Intn(3), r.Intn(1000))})
			}
		case OpAcl:
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: OpAcl, Path: p, Arg: fmt.Sprintf("user:%d:rwx:allow", 2000+r.Intn(5))})
			}
		case OpFlag:
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: OpFlag, Path: p, Arg: FlagNames[r.Intn(len(FlagNames))]})
			}
		default: // OpModify, OpTruncate
			if p, ok := pick(); ok {
				ops = append(ops, Op{Kind: k, Path: p})
			}
		}
	}
	return ops
}

// longSuffix occasionally returns a long path component, covering design 9.4's "long names".
func longSuffix(r *rand.Rand) string {
	if r.Intn(10) != 0 {
		return "n"
	}
	b := make([]byte, 180)
	for i := range b {
		b[i] = 'a' + byte(r.Intn(26))
	}
	return string(b)
}

// Alive replays ops and returns the set of paths that exist afterward (for building the
// expected Tree to compare against).
func Alive(ops []Op) map[string]bool {
	alive := map[string]bool{}
	for _, op := range ops {
		switch op.Kind {
		case OpCreate, OpCreateDir, OpHardlink:
			alive[op.Path] = true
			if op.NewPath != "" {
				alive[op.NewPath] = true
			}
		case OpRename:
			delete(alive, op.Path)
			alive[op.NewPath] = true
		case OpDelete:
			delete(alive, op.Path)
		}
	}
	return alive
}
