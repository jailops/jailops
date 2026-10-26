package datastore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// DirBackend stores objects as files below a root directory.
//
// A write goes to a temporary file under <root>/tmp, is fsynced, and then
// appears under its final name in one step: link(2) for a create-only write
// (it fails with EEXIST, atomically, when the key exists) or rename(2) for a
// replacing one. The parent directory is fsynced afterwards. A reader or a
// crash therefore never sees a partly written object; debris of a crash stays
// in tmp/ and is removed by CleanTmp.
type DirBackend struct {
	root string
	// Hook, when set, is called at named points of a Put of key ("put:created",
	// "put:written", "put:synced", "put:linked", "put:done"). A non-nil error
	// abandons the Put on the spot, leaving the disk exactly as a crash at
	// that point would (tests).
	Hook func(point, key string) error
	// NoSync turns off fsync (benchmarks and throw-away test trees only).
	NoSync bool
}

// NewDirBackend returns a backend rooted at dir, which is created if needed.
func NewDirBackend(dir string) (*DirBackend, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o750); err != nil {
		return nil, err
	}
	return &DirBackend{root: root}, nil
}

// Root returns the root directory.
func (b *DirBackend) Root() string { return b.root }

// Kind implements Backend.
func (b *DirBackend) Kind() string { return "directory" }

func (b *DirBackend) path(key string) (string, error) {
	if err := ValidKey(key); err != nil {
		return "", err
	}
	return filepath.Join(b.root, filepath.FromSlash(key)), nil
}

func (b *DirBackend) hook(point, key string) error {
	if b.Hook != nil {
		return b.Hook(point, key)
	}
	return nil
}

func (b *DirBackend) syncDir(dir string) error {
	if b.NoSync {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// mkdirAllSync creates dir and fsyncs the parent of every directory it created.
func (b *DirBackend) mkdirAllSync(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if fi, err := os.Lstat(d); err == nil {
			if !fi.IsDir() {
				return fmt.Errorf("%w: %s is an object, not a prefix", ErrInvalidKey, d)
			}
			break
		}
		missing = append(missing, d)
		if filepath.Dir(d) == d {
			break
		}
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		if errors.Is(err, syscall.ENOTDIR) {
			return fmt.Errorf("%w: a parent of the key is an object", ErrInvalidKey)
		}
		return err
	}
	for _, d := range missing {
		if err := b.syncDir(filepath.Dir(d)); err != nil {
			return err
		}
	}
	return nil
}

func tempName() string {
	var x [12]byte
	_, _ = rand.Read(x[:])
	return ".w-" + hex.EncodeToString(x[:])
}

// Put implements Backend.
func (b *DirBackend) Put(ctx context.Context, key string, r io.Reader, size int64, opts PutOptions) error {
	dst, err := b.path(key)
	if err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: negative size", ErrSize)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := b.mkdirAllSync(filepath.Dir(dst)); err != nil {
		return err
	}
	tmp := filepath.Join(b.root, "tmp", tempName())
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return err
	}
	cleanup := func() { _ = os.Remove(tmp) }
	if err := b.hook("put:created", key); err != nil {
		f.Close()
		return err // crash: the empty temp file stays
	}
	n, err := io.Copy(f, io.LimitReader(&ctxReader{ctx, r}, size+1))
	if err == nil && n != size {
		err = fmt.Errorf("%w: got %d bytes, declared %d", ErrSize, n, size)
	}
	if err != nil {
		f.Close()
		cleanup()
		return err
	}
	if err := b.hook("put:written", key); err != nil {
		f.Close()
		return err
	}
	if !b.NoSync {
		if err := f.Sync(); err != nil {
			f.Close()
			cleanup()
			return err
		}
	}
	if err := f.Close(); err != nil {
		cleanup()
		return err
	}
	if err := b.hook("put:synced", key); err != nil {
		return err
	}
	if opts.IfAbsent {
		// link fails with EEXIST when dst exists: atomic create-if-absent
		if err := os.Link(tmp, dst); err != nil {
			cleanup()
			if errors.Is(err, fs.ErrExist) {
				return ErrExists
			}
			return fmt.Errorf("create-only write needs hard links: %w", err)
		}
		if err := b.hook("put:linked", key); err != nil {
			return err // crash: the object is complete, the temp link stays
		}
		cleanup()
	} else {
		if err := os.Rename(tmp, dst); err != nil {
			cleanup()
			return err
		}
		if err := b.hook("put:linked", key); err != nil {
			return err
		}
	}
	if err := b.syncDir(filepath.Dir(dst)); err != nil {
		return err
	}
	return b.hook("put:done", key)
}

// Get implements Backend.
func (b *DirBackend) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	return b.GetRange(ctx, key, 0, -1)
}

type sectionFile struct {
	*io.SectionReader
	f *os.File
}

func (s sectionFile) Close() error { return s.f.Close() }

// GetRange implements Backend.
func (b *DirBackend) GetRange(ctx context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		f.Close()
		if err == nil {
			err = ErrNotFound
		}
		return nil, err
	}
	lo, hi, err := clampRange(fi.Size(), offset, length)
	if err != nil {
		f.Close()
		return nil, err
	}
	return sectionFile{io.NewSectionReader(f, lo, hi-lo), f}, nil
}

// Head implements Backend.
func (b *DirBackend) Head(ctx context.Context, key string) (ObjectInfo, error) {
	p, err := b.path(key)
	if err != nil {
		return ObjectInfo{}, err
	}
	if err := ctx.Err(); err != nil {
		return ObjectInfo{}, err
	}
	fi, err := os.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return ObjectInfo{}, ErrNotFound
		}
		return ObjectInfo{}, err
	}
	if fi.IsDir() {
		return ObjectInfo{}, ErrNotFound
	}
	return ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// List implements Backend. Keys come out in lexical order of the whole key
// (a directory sorts as its name followed by '/').
func (b *DirBackend) List(ctx context.Context, prefix string, fn func(ObjectInfo) error) error {
	if err := validPrefix(prefix); err != nil {
		return err
	}
	// the deepest directory that every match lies under
	dirKey := ""
	if i := strings.LastIndex(prefix, "/"); i >= 0 {
		dirKey = prefix[:i]
	}
	start := filepath.Join(b.root, filepath.FromSlash(dirKey))
	if fi, err := os.Stat(start); err != nil || !fi.IsDir() {
		if err == nil || errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			return nil
		}
		return err
	}
	var walk func(dir, keyPrefix string) error
	walk = func(dir, keyPrefix string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		es, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // removed while listing
			}
			return err
		}
		type ent struct {
			sortKey string
			e       fs.DirEntry
		}
		list := make([]ent, 0, len(es))
		for _, e := range es {
			name := e.Name()
			if keyPrefix == "" && name == "tmp" {
				continue
			}
			k := name
			if e.IsDir() {
				k += "/"
			}
			list = append(list, ent{k, e})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].sortKey < list[j].sortKey })
		for _, x := range list {
			full := keyPrefix + x.sortKey
			if x.e.IsDir() {
				// skip directories that cannot hold a match
				if !strings.HasPrefix(full, prefix) && !strings.HasPrefix(prefix, full) {
					continue
				}
				if err := walk(filepath.Join(dir, x.e.Name()), full); err != nil {
					return err
				}
				continue
			}
			if !strings.HasPrefix(full, prefix) || x.e.Type()&fs.ModeType != 0 {
				continue
			}
			info, err := x.e.Info()
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue
				}
				return err
			}
			if err := fn(ObjectInfo{Key: full, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
		}
		return nil
	}
	kp := ""
	if dirKey != "" {
		kp = dirKey + "/"
	}
	return walk(start, kp)
}

// Delete implements Backend.
func (b *DirBackend) Delete(ctx context.Context, key string) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
		return nil // a prefix, not an object
	}
	err = os.Remove(p)
	switch {
	case err == nil:
		return b.syncDir(filepath.Dir(p))
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
		return nil
	}
	return err
}

// CleanTmp removes crash debris in tmp/ older than olderThan and returns how
// many files it removed.
func (b *DirBackend) CleanTmp(olderThan time.Duration) (int, error) {
	cutoff := time.Now().Add(-olderThan)
	es, err := os.ReadDir(filepath.Join(b.root, "tmp"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range es {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(b.root, "tmp", e.Name())) == nil {
			n++
		}
	}
	return n, nil
}

// TempFiles lists the files in tmp/ (tests).
func (b *DirBackend) TempFiles() []string {
	es, _ := os.ReadDir(filepath.Join(b.root, "tmp"))
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
