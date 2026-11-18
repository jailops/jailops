package priv

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// TreeDiffReq asks for the files that differ between the tree below Dir and the tree below Base: what a jail's own
// directory holds that the image does not, for a directory that has no snapshot to compare with. Both paths lie below
// a storage mount; the answer lists the paths below Dir.
type TreeDiffReq struct {
	Dir, Base string
	Limit     int
}

// treeDiffMax is the most entries read on each side and treeDiffTime the time the walk may take: a jail with a million
// files answers with a cut list and not with a stuck helper.
const (
	treeDiffMax  = 500000
	treeDiffTime = time.Minute
)

// treeEntry is what the comparison looks at: the kind of file, its permission bits, its size (files only) and its
// modification time. The copy that seeded a persistent directory keeps all of them, so an untouched file is equal.
type treeEntry struct {
	kind  string
	mode  fs.FileMode
	size  int64
	mtime int64
}

func treeKind(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "/"
	case m&fs.ModeSymlink != 0:
		return "@"
	case m.IsRegular():
		return "F"
	default:
		return "?"
	}
}

// TreeDiff implements Host. It reads metadata only: no file is opened and no link is followed.
func (l *Local) TreeDiff(ctx context.Context, q TreeDiffReq) (zfs.DiffResult, error) {
	for _, p := range []string{q.Dir, q.Base} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || !l.cfg.inMounts(p) {
			return zfs.DiffResult{}, forbidden("tree diff: %q is not a path below a storage mount", p)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, treeDiffTime)
	defer cancel()
	have, err := walkTree(ctx, q.Dir)
	if err != nil {
		return zfs.DiffResult{}, err
	}
	base, err := walkTree(ctx, q.Base)
	if err != nil {
		return zfs.DiffResult{}, err
	}
	return compareTrees(q.Dir, have, base, q.Limit), nil
}

func walkTree(ctx context.Context, root string) (map[string]treeEntry, error) {
	out := map[string]treeEntry{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err != nil {
			if p == root {
				return err
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if rel == "." {
			return nil
		}
		if len(out) >= treeDiffMax {
			return fs.SkipAll
		}
		fi, err := d.Info()
		if err != nil {
			return nil
		}
		out[rel] = treeEntry{kind: treeKind(fi.Mode()), mode: fi.Mode().Perm(), size: fi.Size(), mtime: fi.ModTime().Unix()}
		return nil
	})
	if err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "no such file") {
		return out, nil // a directory the image does not have: everything in the jail's is new
	}
	return out, err
}

func compareTrees(dir string, have, base map[string]treeEntry, limit int) zfs.DiffResult {
	var lines []zfs.DiffEntry
	for rel, h := range have {
		b, ok := base[rel]
		switch {
		case !ok:
			lines = append(lines, zfs.DiffEntry{Kind: "+", Type: h.kind, Path: filepath.Join(dir, rel)})
		case h.kind == "/" && b.kind == "/":
			// a directory that is in both is not a change; what is below it says what changed
		case h.kind != b.kind || h.mode != b.mode || (h.kind == "F" && (h.size != b.size || h.mtime != b.mtime)) || (h.kind == "@" && h.mtime != b.mtime):
			lines = append(lines, zfs.DiffEntry{Kind: "M", Type: h.kind, Path: filepath.Join(dir, rel)})
		}
	}
	for rel, b := range base {
		if _, ok := have[rel]; !ok {
			lines = append(lines, zfs.DiffEntry{Kind: "-", Type: b.kind, Path: filepath.Join(dir, rel)})
		}
	}
	sort.Slice(lines, func(i, j int) bool { return lines[i].Path < lines[j].Path })
	if limit <= 0 || limit > api.MaxDiffLimit {
		limit = api.MaxDiffLimit
	}
	res := zfs.DiffResult{Entries: []zfs.DiffEntry{}, Total: len(lines), Counts: map[string]int{}}
	for _, e := range lines {
		res.Counts[e.Kind]++
	}
	if len(lines) > limit {
		res.Truncated = true
		lines = lines[:limit]
	}
	res.Entries = append(res.Entries, lines...)
	return res
}
