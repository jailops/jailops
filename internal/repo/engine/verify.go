package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"jailops.net/jailops/internal/repo/catalog"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/repo/keys"
)

// Verification modes, in increasing depth.
const (
	// VerifyDeep downloads and decrypts every distinct chunk and recomputes its
	// id: it proves restorability of the data and finds missing, damaged and
	// poisoned chunks (a self-consistent blob planted under a wrong id).
	VerifyDeep = "deep"
	// VerifyExtract restores every stream into a scratch directory, checking each
	// file's SHA-256 while it is written, then reads the files back from disk and
	// checks them again, and compares the entry counts with the catalog.
	VerifyExtract = "extract"
	// VerifyFull restores with ownership, attributes and file flags where the
	// platform allows and compares the metadata of the restored tree with the
	// catalog. A health check of a restored jail is the host's job.
	VerifyFull = "full"
)

// VerifyOptions select what to verify.
type VerifyOptions struct {
	Key      *keys.Key
	Repo     Repository
	Group    string
	Snapshot string // "" = the latest
	Mode     string // VerifyDeep (default), VerifyExtract or VerifyFull
	// Scratch is the directory under which extract and full verification create
	// their temporary tree (default: the system temporary directory).
	Scratch string
	Attrs   catalog.Attrs
	Workers int
	Window  int
}

// BadChunk is a chunk that failed verification.
type BadChunk struct {
	ID     format.ID
	Reason string
}

// VerifyResult reports a verification. OK is true when nothing is wrong.
type VerifyResult struct {
	Snapshot string
	Mode     string
	OK       bool
	Chunks   int    // distinct chunks checked (deep)
	Bytes    uint64 // plaintext bytes checked
	Bad      []BadChunk
	Files    int
	Problems []string
	Notes    []string
}

// Verify checks a snapshot. Failures of the data are reported in the result, not
// as an error; an error means the check itself could not run (a wrong key, a
// missing snapshot, a tampered index or manifest).
func Verify(ctx context.Context, o VerifyOptions) (*VerifyResult, error) {
	if o.Key == nil {
		return nil, ErrNoKey
	}
	if o.Mode == "" {
		o.Mode = VerifyDeep
	}
	snap, err := OpenSnapshot(ctx, o.Repo, o.Key.Keys, o.Group, o.Snapshot)
	if err != nil {
		return nil, err
	}
	res := &VerifyResult{Snapshot: snap.ID, Mode: o.Mode}
	switch o.Mode {
	case VerifyDeep:
		err = verifyDeep(ctx, snap, o, res)
	case VerifyExtract, VerifyFull:
		err = verifyRestore(ctx, snap, o, res)
	default:
		return nil, fmt.Errorf("engine: unknown verify mode %q", o.Mode)
	}
	if err != nil {
		return nil, err
	}
	res.OK = len(res.Bad) == 0 && len(res.Problems) == 0
	return res, nil
}

func verifyDeep(ctx context.Context, snap *Snapshot, o VerifyOptions, res *VerifyResult) error {
	sizes := map[format.ID]uint64{}
	var order []format.ID
	for i, e := range snap.Index.Entries {
		if e.ID.IsZero() {
			continue
		}
		_, n := snap.Index.Span(i)
		if prev, ok := sizes[e.ID]; ok {
			if prev != n {
				res.Problems = append(res.Problems, fmt.Sprintf("chunk %s is used with two different sizes (%d and %d)", e.ID, prev, n))
			}
			continue
		}
		sizes[e.ID] = n
		order = append(order, e.ID)
	}
	f := newFetcher(snap.keys, snap.repo, o.Workers, o.Window)
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, f.workers)
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, id := range order {
		select {
		case sem <- struct{}{}:
		case <-cctx.Done():
			return cctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			_, err := f.chunk(cctx, id, sizes[id])
			mu.Lock()
			defer mu.Unlock()
			res.Chunks++
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				res.Bad = append(res.Bad, BadChunk{ID: id, Reason: err.Error()})
				return
			}
			res.Bytes += sizes[id]
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	sortBad(res.Bad)
	// the catalogs must decode and agree with the payload ranges
	for i := range snap.Manifest.Streams {
		st := &snap.Manifest.Streams[i]
		t, err := snap.LoadCatalog(ctx, st, o.Workers, o.Window)
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: %v", st.Name, err))
			continue
		}
		if t.Count != st.Entries {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: the catalog has %d entries, the manifest says %d", st.Name, t.Count, st.Entries))
		}
		if err := checkCatalogRanges(t, st.PayloadSize); err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: %v", st.Name, err))
		}
		res.Files += t.Stats().Files
	}
	return nil
}

func sortBad(b []BadChunk) {
	for i := 1; i < len(b); i++ {
		for j := i; j > 0 && b[j].ID.String() < b[j-1].ID.String(); j-- {
			b[j], b[j-1] = b[j-1], b[j]
		}
	}
}

func verifyRestore(ctx context.Context, snap *Snapshot, o VerifyOptions, res *VerifyResult) error {
	scratch, err := os.MkdirTemp(o.Scratch, "jailops-verify-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	root := os.Geteuid() == 0
	for i := range snap.Manifest.Streams {
		st := &snap.Manifest.Streams[i]
		dest := filepath.Join(scratch, fmt.Sprintf("s%d", i))
		if err := os.Mkdir(dest, 0o700); err != nil {
			return err
		}
		skips := &catalog.Skips{}
		eo := catalog.ExtractOptions{NoChown: o.Mode == VerifyExtract || !root, Lenient: o.Mode == VerifyFull, Skips: skips, Attrs: o.Attrs, SkipDevices: !root}
		tree, err := snap.restoreWhole(ctx, st, catalog.LocalSink(dest, o.Attrs), eo, o.Workers, o.Window)
		if err != nil {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: restore failed: %v", st.Name, err))
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if s := skips.Summary(); s != "" {
			res.Notes = append(res.Notes, fmt.Sprintf("stream %s: %s", st.Name, s))
		}
		if !root && o.Mode == VerifyFull {
			res.Notes = append(res.Notes, "not running as root: ownership could not be restored or compared")
		}
		n, probs := compareRestored(tree, dest, o.Mode == VerifyFull, root, o.Attrs)
		res.Files += tree.Stats().Files
		res.Bytes += st.PayloadSize
		for _, p := range probs {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: %s", st.Name, p))
		}
		if n != tree.Count-1 {
			res.Problems = append(res.Problems, fmt.Sprintf("stream %s: %d entries on disk, the catalog has %d", st.Name, n, tree.Count-1))
		}
	}
	return nil
}

// compareRestored reads the restored tree back from disk and checks it against
// the catalog: every file's SHA-256 again, and with deep set the metadata.
func compareRestored(t *catalog.Tree, dest string, deep, root bool, attrs catalog.Attrs) (entries int, problems []string) {
	if attrs == nil {
		attrs = catalog.DefaultAttrs()
	}
	add := func(f string, a ...any) {
		if len(problems) < 50 {
			problems = append(problems, fmt.Sprintf(f, a...))
		}
	}
	t.Walk(func(rel string, e *catalog.Entry) bool {
		if rel == "" {
			return true
		}
		entries++
		p := filepath.Join(dest, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			add("%s: missing from the restored tree (%v)", rel, err)
			return true
		}
		switch e.Kind {
		case catalog.KFile, catalog.KHardlink:
			src := e
			if e.Kind == catalog.KHardlink {
				var lerr error
				if src, lerr = t.Lookup(e.Link); lerr != nil {
					add("%s: hard link target %q missing", rel, e.Link)
					return true
				}
			}
			if !fi.Mode().IsRegular() {
				add("%s: not a regular file after restore", rel)
				return true
			}
			if uint64(fi.Size()) != src.Size {
				add("%s: %d bytes on disk, the catalog says %d", rel, fi.Size(), src.Size)
				return true
			}
			if src.Size > 0 {
				sum, err := hashFile(p)
				if err != nil {
					add("%s: %v", rel, err)
				} else if sum != src.Sum {
					add("%s: content on disk does not match the catalog SHA-256", rel)
				}
			}
		case catalog.KSymlink:
			if l, err := os.Readlink(p); err != nil || l != e.Link {
				add("%s: symlink target %q, the catalog says %q", rel, l, e.Link)
			}
		case catalog.KDir:
			if !fi.IsDir() {
				add("%s: not a directory after restore", rel)
			}
		}
		if !deep || e.Kind == catalog.KHardlink {
			return true
		}
		if e.MtimeNs != 0 && e.Kind != catalog.KSymlink {
			if got := fi.ModTime().UnixNano(); got != e.MtimeNs {
				add("%s: mtime %d, the catalog says %d", rel, got, e.MtimeNs)
			}
		}
		if e.Kind != catalog.KSymlink && uint32(fi.Mode().Perm()) != e.Mode&0o777 && !(e.Mode&0o7000 != 0 && !root) {
			add("%s: mode %o, the catalog says %o", rel, fi.Mode().Perm(), e.Mode&0o777)
		}
		if root {
			if st, ok := fi.Sys().(*syscall.Stat_t); ok && (st.Uid != e.UID || st.Gid != e.GID) {
				add("%s: owner %d:%d, the catalog says %d:%d", rel, st.Uid, st.Gid, e.UID, e.GID)
			}
		}
		if len(e.Attrs) > 0 {
			got, err := attrs.Collect(p)
			if err != nil {
				add("%s: reading attributes back: %v", rel, err)
			} else {
				have := map[string]string{}
				for _, a := range got {
					have[a.Name] = string(a.Value)
				}
				for _, a := range e.Attrs {
					if v, ok := have[a.Name]; ok && v != string(a.Value) {
						add("%s: attribute %s differs after restore", rel, a.Name)
					}
				}
			}
		}
		return true
	})
	return entries, problems
}

func hashFile(p string) ([32]byte, error) {
	var out [32]byte
	f, err := os.Open(p)
	if err != nil {
		return out, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	copy(out[:], h.Sum(nil))
	return out, nil
}
