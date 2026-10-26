package catalog

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// Payload gives access to the bytes of the payload stream.
type Payload interface {
	// Open returns a reader for payload bytes [off, off+size).
	Open(off, size uint64) (io.Reader, error)
}

// FromReaderAt adapts a random-access reader (the chunk store) to Payload.
func FromReaderAt(r io.ReaderAt) Payload { return readerAt{r} }

type readerAt struct{ r io.ReaderAt }

func (p readerAt) Open(off, size uint64) (io.Reader, error) {
	return io.NewSectionReader(p.r, int64(off), int64(size)), nil
}

// Sequential adapts a forward-only stream that starts at payload offset 0.
// Entries must be opened in increasing offset order; gaps (files that are not
// extracted) are skipped by reading and discarding.
func Sequential(r io.Reader) Payload { return &seq{r: r} }

type seq struct {
	r   io.Reader
	pos uint64
	cur *io.LimitedReader
}

func (s *seq) Open(off, size uint64) (io.Reader, error) {
	if s.cur != nil && s.cur.N > 0 { // finish the previous entry (the counting reader advances pos)
		if _, err := io.Copy(io.Discard, s.cur); err != nil {
			return nil, err
		}
	}
	if off < s.pos {
		return nil, fmt.Errorf("payload stream is forward-only (at %d, asked for %d)", s.pos, off)
	}
	if off > s.pos {
		n, err := io.CopyN(io.Discard, s.r, int64(off-s.pos))
		s.pos += uint64(n)
		if err != nil {
			return nil, fmt.Errorf("payload ends early: %w", err)
		}
	}
	s.cur = &io.LimitedReader{R: &countReader{r: s.r, pos: &s.pos}, N: int64(size)}
	return s.cur, nil
}

type countReader struct {
	r   io.Reader
	pos *uint64
}

func (c *countReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.pos += uint64(n)
	return n, err
}

// ExtractOptions control extraction.
type ExtractOptions struct {
	// NoChown skips changing ownership and tolerates attributes and devices
	// that need privileges: for extracting as an ordinary user.
	NoChown bool
	// Paths restricts extraction to these paths and everything below them
	// (empty = the whole tree). Parent directories are created with their
	// catalogued metadata.
	Paths []string
	// SkipVerify turns off the SHA-256 check of extracted content (the default
	// checks every file).
	SkipVerify bool
	// SkipDevices creates no character or block device nodes (fifos are still
	// created): for extracting an untrusted catalog as root into a scratch directory.
	SkipDevices bool
	// Lenient skips, and records in Skips, ownership and attribute operations the
	// kernel refuses with EPERM, EINVAL or ENOTSUP (a user namespace, an LSM, a
	// filesystem without the feature) instead of aborting. A file whose chown was
	// skipped loses its set-id bits. Without Lenient any failure is an error.
	// Lenient needs Skips.
	Lenient bool
	// Skips collects what was skipped (attributes or flags the platform cannot
	// apply, and, with Lenient, refused operations). It may be set without Lenient.
	Skips *Skips
	// Attrs applies extended attributes and ACLs (nil = the platform's).
	Attrs Attrs
}

// Skips records the metadata operations an extraction did not perform: how
// many, and the first few.
type Skips struct {
	mu    sync.Mutex
	Count int
	First []string // at most maxSkipExamples
}

const maxSkipExamples = 5

func (k *Skips) add(msg string) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.Count++
	if len(k.First) < maxSkipExamples {
		k.First = append(k.First, msg)
	}
}

// Summary is a one-line description ("" when nothing was skipped).
func (k *Skips) Summary() string {
	if k == nil {
		return ""
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.Count == 0 {
		return ""
	}
	return fmt.Sprintf("%d metadata operation(s) were not applied (first: %s)", k.Count, strings.Join(k.First, "; "))
}

// SafePath joins a catalog path onto root and refuses anything that could land
// outside it: empty, "." or ".." components, and any existing parent that is not
// a real directory (a symlink planted by an earlier entry or by someone else).
func SafePath(root, name string) (string, error) {
	if name == "" {
		return root, nil
	}
	parts := strings.Split(name, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." || strings.ContainsRune(c, 0) {
			return "", fmt.Errorf("unsafe path %q", name)
		}
	}
	cur := root
	for i, c := range parts {
		cur = filepath.Join(cur, c)
		if i == len(parts)-1 {
			break
		}
		fi, err := os.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("parent of %q does not exist: %w", name, err)
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("parent %q of %q is not a directory (symlink traversal refused)", cur, name)
		}
	}
	return cur, nil
}

type item struct {
	rel   string
	e     *Entry
	whole bool // false: an ancestor directory created only to hold selected entries
}

// Sink receives an extraction. rel is a clean path inside the tree ("" is the destination itself); an
// implementation refuses anything that could land outside its destination. The local sink writes to a
// directory; the agent sink (internal/fsagent) asks a process that holds the privileges to write one, for a
// caller that does not.
type Sink interface {
	// Begin checks that the destination exists and is a directory.
	Begin() error
	// Mkdir creates a directory; an existing directory is fine, anything else is refused.
	Mkdir(rel string) error
	// WriteFile creates rel exclusively and writes exactly size bytes read from r. With a sum it checks the
	// SHA-256 of what was written and fails on a mismatch.
	WriteFile(rel string, size uint64, sum *[32]byte, r io.Reader) error
	Symlink(rel, target string) error
	// Link makes rel a hard link to target, another path of the tree, which must be a regular file.
	Link(rel, target string) error
	// Mknod creates a device node or a fifo. The kernel's refusal (EPERM) is returned as such.
	Mknod(rel string, mode uint32, major, minor uint32) error
	// Apply sets the ownership, attributes, times, mode and flags of rel from e; link says rel is a symlink,
	// which is acted on itself. It returns what it skipped (see MetaOptions).
	Apply(rel string, e *Entry, link bool, o MetaOptions) (skipped []string, err error)
}

// MetaOptions are the options of Sink.Apply.
type MetaOptions struct {
	// NoChown: ownership is not restored (see ExtractOptions.NoChown).
	NoChown bool
	// Lenient tolerates and reports what the kernel refuses (see ExtractOptions.Lenient).
	Lenient bool
}

// Extract writes the selected part of the tree below dest (which must exist).
// It never follows symlinks and refuses unsafe paths.
func (t *Tree) Extract(dest string, pay Payload, o ExtractOptions) error {
	if o.Attrs == nil {
		o.Attrs = DefaultAttrs()
	}
	return t.ExtractTo(LocalSink(dest, o.Attrs), pay, o)
}

// ExtractTo is Extract into any Sink. o.Attrs is not used (the sink applies attributes).
func (t *Tree) ExtractTo(sink Sink, pay Payload, o ExtractOptions) error {
	if o.Lenient && o.Skips == nil {
		return errors.New("catalog: Lenient extraction needs Skips to record what it skipped")
	}
	if err := sink.Begin(); err != nil {
		return err
	}
	mo := MetaOptions{NoChown: o.NoChown, Lenient: o.Lenient}
	selected := map[string]bool{}
	for _, p := range o.Paths {
		c, err := CleanPath(p)
		if err != nil {
			return err
		}
		if _, err := t.Lookup(c); err != nil {
			return err
		}
		selected[c] = true
	}
	inSelection := func(rel string) bool {
		if len(selected) == 0 {
			return true
		}
		for s := range selected {
			if s == "" || rel == s || strings.HasPrefix(rel, s+"/") {
				return true
			}
		}
		return false
	}
	isAncestor := func(rel string) bool {
		for s := range selected {
			if rel == "" || strings.HasPrefix(s, rel+"/") {
				return true
			}
		}
		return false
	}
	var items []item
	extracted := map[string]bool{}
	t.Walk(func(rel string, e *Entry) bool {
		switch {
		case inSelection(rel):
			items = append(items, item{rel, e, true})
			extracted[rel] = true
			return true
		case e.Kind == KDir && isAncestor(rel):
			items = append(items, item{rel, e, false})
			return true
		}
		return false
	})
	apply := func(rel string, e *Entry, link bool) error {
		skipped, err := sink.Apply(rel, e, link, mo)
		for _, m := range skipped {
			o.Skips.add(m)
		}
		return err
	}
	var dirs []item
	for _, it := range items {
		if it.rel == "" {
			dirs = append(dirs, it) // the destination itself: metadata applied last
			continue
		}
		e := it.e
		switch e.Kind {
		case KDir:
			if err := sink.Mkdir(it.rel); err != nil {
				return err
			}
			dirs = append(dirs, it)
		case KFile:
			if err := extractFile(sink, it.rel, e, pay, o); err != nil {
				return err
			}
			if err := apply(it.rel, e, false); err != nil {
				return err
			}
		case KSymlink:
			if err := sink.Symlink(it.rel, e.Link); err != nil {
				return err
			}
			if err := apply(it.rel, e, true); err != nil {
				return err
			}
		case KHardlink:
			if extracted[e.Link] {
				if err := sink.Link(it.rel, e.Link); err != nil {
					return err
				}
				break
			}
			// the first link was not selected: materialise this one as an ordinary file
			first, err := t.Lookup(e.Link)
			if err != nil || first.Kind != KFile {
				return fmt.Errorf("hard link %s points at %q which is not in the catalog", it.rel, e.Link)
			}
			cp := *first
			cp.Name = e.Name
			if err := extractFile(sink, it.rel, &cp, pay, o); err != nil {
				return err
			}
			if err := apply(it.rel, &cp, false); err != nil {
				return err
			}
		case KChar, KBlock, KFifo:
			if o.SkipDevices && e.Kind != KFifo {
				continue
			}
			if err := sink.Mknod(it.rel, e.Mode|typeBits(e.Kind), e.Major, e.Minor); err != nil {
				if (o.NoChown || o.Lenient) && errors.Is(err, unix.EPERM) {
					o.Skips.add(fmt.Sprintf("%s: %s node needs privileges", it.rel, e.Kind))
					break // not root: devices cannot be created; the rest is restored
				}
				return fmt.Errorf("mknod %s: %w", it.rel, err)
			}
			if err := apply(it.rel, e, false); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s: unknown entry kind %d", it.rel, e.Kind)
		}
	}
	// directories last, children first, so their mtimes survive the writes above
	for i := len(dirs) - 1; i >= 0; i-- {
		if dirs[i].whole || dirs[i].rel != "" {
			if err := apply(dirs[i].rel, dirs[i].e, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func extractFile(sink Sink, rel string, e *Entry, pay Payload, o ExtractOptions) error {
	var r io.Reader = bytes.NewReader(nil)
	if e.Size > 0 {
		pr, err := pay.Open(e.PayOff, e.Size)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		r = pr
	}
	var sum *[32]byte
	if !o.SkipVerify {
		sum = &e.Sum
	}
	if err := sink.WriteFile(rel, e.Size, sum, r); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	return nil
}

// ---- the local sink ----

// LocalSink writes the extraction into the directory dest of the local file system, applying attributes
// with attrs (nil = the platform's).
func LocalSink(dest string, attrs Attrs) Sink {
	if attrs == nil {
		attrs = DefaultAttrs()
	}
	return &localSink{dest: dest, attrs: attrs}
}

type localSink struct {
	dest  string
	attrs Attrs
}

func (s *localSink) Begin() error {
	if fi, err := os.Lstat(s.dest); err != nil || !fi.IsDir() {
		return fmt.Errorf("extraction target %s is not a directory", s.dest)
	}
	return nil
}

func (s *localSink) Mkdir(rel string) error {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return err
	}
	if err := os.Mkdir(p, 0o700); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		if fi, lerr := os.Lstat(p); lerr != nil || !fi.IsDir() {
			return fmt.Errorf("%s exists and is not a directory (refusing to follow it)", rel)
		}
	}
	return nil
}

func (s *localSink) WriteFile(rel string, size uint64, sum *[32]byte, r io.Reader) error {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if size > 0 {
		h := sha256.New()
		var w io.Writer = f
		if sum != nil {
			w = io.MultiWriter(f, h)
		}
		n, err := io.Copy(w, r)
		if err != nil {
			f.Close()
			return err
		}
		if uint64(n) != size {
			f.Close()
			return fmt.Errorf("payload delivered %d bytes, the catalog says %d", n, size)
		}
		if sum != nil {
			var got [32]byte
			copy(got[:], h.Sum(nil))
			if got != *sum {
				f.Close()
				return errors.New("content does not match its recorded SHA-256 (the repository returned wrong data)")
			}
		}
	}
	return f.Close()
}

func (s *localSink) Symlink(rel, target string) error {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return err
	}
	return os.Symlink(target, p)
}

func (s *localSink) Link(rel, target string) error {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return err
	}
	t, err := SafePath(s.dest, target)
	if err != nil {
		return err
	}
	if fi, err := os.Lstat(t); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("hard link %s: the first link %q is not a regular file", rel, target)
	}
	return os.Link(t, p)
}

func (s *localSink) Mknod(rel string, mode uint32, major, minor uint32) error {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return err
	}
	return mknod(p, mode, major, minor)
}

func (s *localSink) Apply(rel string, e *Entry, link bool, o MetaOptions) ([]string, error) {
	p, err := SafePath(s.dest, rel)
	if err != nil {
		return nil, err
	}
	var skips []string
	add := func(msg string) { skips = append(skips, msg) }
	return skips, applyMeta(p, e, link, o, s.attrs, add)
}

// seams for tests: the call whose failure Lenient tolerates.
var lchown = os.Lchown

func tolerableErr(err error) bool {
	return errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP)
}

// extractingUID is the uid files are created as when ownership is not restored (a seam for tests).
var extractingUID = os.Geteuid

func applyMeta(p string, e *Entry, link bool, o MetaOptions, attrs Attrs, skip func(string)) error {
	if fi, err := os.Lstat(p); err != nil || (!link && fi.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("%s is a symlink or vanished: refusing to apply metadata through it", p)
	}
	owned, chownSkipped := !o.NoChown, false
	if owned {
		if err := lchown(p, int(e.UID), int(e.GID)); err != nil {
			if !o.Lenient || !tolerableErr(err) {
				return fmt.Errorf("chown %s: %w", p, err)
			}
			skip(fmt.Sprintf("chown %d:%d %s: %v", e.UID, e.GID, filepath.Base(p), err))
			owned, chownSkipped = false, true
		}
	}
	if len(e.Attrs) > 0 {
		skipped, err := attrs.Apply(p, e.Attrs, ApplyOptions{NoChown: !owned, Lenient: o.Lenient})
		for _, s := range skipped {
			skip(fmt.Sprintf("%s on %s: %s", s.Name, filepath.Base(p), s.Reason))
		}
		if err != nil {
			return fmt.Errorf("attributes of %s: %w", p, err)
		}
	}
	ts := []unix.Timespec{unix.NsecToTimespec(e.MtimeNs), unix.NsecToTimespec(e.MtimeNs)}
	if link {
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
	} else {
		m := e.Mode
		// ownership not restored: never hand the extracting user (root) a setuid or setgid file
		if !owned && (chownSkipped || int(e.UID) != extractingUID()) {
			m &^= 0o4000
			if e.Kind != KDir {
				m &^= 0o2000 // a setgid directory only steers group inheritance
			}
		}
		if err := os.Chmod(p, fileMode(m)); err != nil {
			return err
		}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, p, ts, 0); err != nil {
			return err
		}
	}
	if e.Flags != 0 {
		applied, err := setFlags(p, e.Flags, link)
		switch {
		case err != nil && !(o.Lenient && tolerableErr(err)):
			return fmt.Errorf("file flags of %s: %w", p, err)
		case err != nil:
			skip(fmt.Sprintf("file flags %#x on %s: %v", e.Flags, filepath.Base(p), err))
		case !applied:
			skip(fmt.Sprintf("file flags %#x on %s: this platform has no file flags", e.Flags, filepath.Base(p)))
		}
	}
	return nil
}

func fileMode(m uint32) os.FileMode {
	mode := os.FileMode(m & 0o777)
	if m&0o4000 != 0 {
		mode |= os.ModeSetuid
	}
	if m&0o2000 != 0 {
		mode |= os.ModeSetgid
	}
	if m&0o1000 != 0 {
		mode |= os.ModeSticky
	}
	return mode
}
