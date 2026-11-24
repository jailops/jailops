// Package tarcopy copies files and directory trees as tar streams between two directories that are each held as an
// os.Root. It is the one implementation behind `jailops cp`: the privileged helper uses it for a jail root, the
// command line for a local directory, so what is refused is refused the same way on both sides.
//
// The rules, in one place:
//
//   - A symlink is never followed below a root. A path component that is a symlink is refused, and a symlink in an
//     archive is created as a symlink (its target is only text until the jail itself follows it).
//   - Names in an archive are relative and clean: an absolute name, a ".." segment, an empty segment or a NUL byte
//     is refused, and so is a name deeper than MaxDepth.
//   - Only regular files, directories and symlinks are copied. Devices, FIFOs and sockets are skipped when a tree is
//     written out, and refused when they arrive in an archive. A hard link in an archive is refused.
//   - The set-user-id, set-group-id and sticky bits are never set. Owner and times are written only with Preserve.
//   - A regular file is written to a temporary name and renamed over the target, so a reader never sees half a file
//     and a target that is a symlink is replaced, not followed. A directory is never replaced by a file.
//     The temporary file is created exclusive, 0600 and owned by the writer; its mode and owner are set to the final ones
//     before the rename, a failing chmod or chown is an error, and a failed write removes it at once. A writer that was
//     killed leaves it behind: the next write in the same directory removes a leftover whose writer no longer exists.
//   - The number of bytes and of entries is bounded.
package tarcopy

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Defaults of Limits.
const (
	DefaultMaxEntries = 100000
	DefaultMaxDepth   = 64
	// MaxNameBytes bounds the path of one entry.
	MaxNameBytes = 4096
)

// ErrRefused marks what the rules above refuse: the copy stopped because of the names, types or links of the data,
// not because of a failing disk.
var ErrRefused = errors.New("refused")

// ErrTooLarge marks a copy that went over its byte or entry limit.
var ErrTooLarge = errors.New("too large")

// ErrNotExist is returned by Create for a source that is missing.
var ErrNotExist = fs.ErrNotExist

func refused(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrRefused, fmt.Sprintf(format, a...))
}

// Limits bound one copy. A zero field is the default; MaxBytes zero is no limit (the caller sets one).
type Limits struct {
	MaxBytes   int64
	MaxEntries int
	MaxDepth   int
}

func (l Limits) entries() int {
	if l.MaxEntries > 0 {
		return l.MaxEntries
	}
	return DefaultMaxEntries
}

func (l Limits) depth() int {
	if l.MaxDepth > 0 {
		return l.MaxDepth
	}
	return DefaultMaxDepth
}

// Stats says what a copy did.
type Stats struct {
	Files    int   `json:"files"`
	Dirs     int   `json:"dirs"`
	Symlinks int   `json:"symlinks"`
	Skipped  int   `json:"skipped,omitempty"` // devices, FIFOs and sockets left out of a tree
	Bytes    int64 `json:"bytes"`
}

// ---- reading a tree out ----

// CreateOpts are the options of Create.
type CreateOpts struct {
	// Recursive allows a directory as the source. Without it a directory is refused.
	Recursive bool
	// Preserve writes owner, group and times into the archive.
	Preserve bool
	Limits   Limits
}

// Create writes the file, directory tree or symlink name, found in dir, to w as a tar stream whose entries are named
// below as ("" is the base name of name). dir is the directory that holds name, as a root; name is one path
// component. A missing name is ErrNotExist.
func Create(dir *os.Root, name, as string, w io.Writer, o CreateOpts) (Stats, error) {
	if name == "" || name == "." || strings.ContainsAny(name, "/\x00") {
		return Stats{}, refused("%q is not one name", name)
	}
	if as == "" {
		as = name
	}
	if err := checkName(as, o.Limits.depth()); err != nil {
		return Stats{}, err
	}
	st, err := dir.Lstat(name)
	if err != nil {
		return Stats{}, err
	}
	tw := tar.NewWriter(w)
	c := &creator{tw: tw, o: o}
	if err := c.entry(dir, name, as, st, 0); err != nil {
		return c.st, err
	}
	if err := tw.Close(); err != nil {
		return c.st, err
	}
	return c.st, nil
}

type creator struct {
	tw *tar.Writer
	o  CreateOpts
	st Stats
	n  int
}

func (c *creator) count() error {
	c.n++
	if c.n > c.o.Limits.entries() {
		return fmt.Errorf("%w: more than %d entries", ErrTooLarge, c.o.Limits.entries())
	}
	return nil
}

func (c *creator) header(st fs.FileInfo, name string, typ byte) *tar.Header {
	h := &tar.Header{Name: name, Typeflag: typ, Mode: int64(st.Mode().Perm()), Format: tar.FormatPAX}
	if c.o.Preserve {
		h.ModTime = st.ModTime()
		if uid, gid, ok := owner(st); ok {
			h.Uid, h.Gid = uid, gid
		}
	}
	return h
}

func (c *creator) entry(dir *os.Root, name, as string, st fs.FileInfo, depth int) error {
	if err := c.count(); err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		target, err := dir.Readlink(name)
		if err != nil {
			return err
		}
		h := c.header(st, as, tar.TypeSymlink)
		h.Linkname = target
		if err := c.tw.WriteHeader(h); err != nil {
			return err
		}
		c.st.Symlinks++
		return nil
	case st.IsDir():
		if depth == 0 && !c.o.Recursive {
			return refused("%s is a directory (copy it with -r)", name)
		}
		if depth >= c.o.Limits.depth() {
			return refused("%s is more than %d directories deep", as, c.o.Limits.depth())
		}
		if err := c.tw.WriteHeader(c.header(st, as+"/", tar.TypeDir)); err != nil {
			return err
		}
		c.st.Dirs++
		sub, err := openDir(dir, name, st)
		if err != nil {
			return err
		}
		defer sub.Close()
		names, err := readNames(sub)
		if err != nil {
			return err
		}
		for _, n := range names {
			cst, err := sub.Lstat(n)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					continue // removed while it was read
				}
				return err
			}
			if err := c.entry(sub, n, as+"/"+n, cst, depth+1); err != nil {
				return err
			}
		}
		return nil
	case st.Mode().IsRegular():
		f, err := dir.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		fst, err := f.Stat()
		if err != nil {
			return err
		}
		if !fst.Mode().IsRegular() || !os.SameFile(st, fst) {
			return refused("%s changed while it was copied", name)
		}
		if max := c.o.Limits.MaxBytes; max > 0 && c.st.Bytes+fst.Size() > max {
			return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
		}
		h := c.header(fst, as, tar.TypeReg)
		h.Size = fst.Size()
		if err := c.tw.WriteHeader(h); err != nil {
			return err
		}
		n, err := io.CopyN(c.tw, f, fst.Size())
		c.st.Bytes += n
		if err != nil {
			if errors.Is(err, io.EOF) {
				return refused("%s became shorter while it was copied", name)
			}
			return err
		}
		c.st.Files++
		return nil
	default:
		c.st.Skipped++
		return nil
	}
}

func readNames(d *os.Root) ([]string, error) {
	f, err := d.Open(".")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

// openDir opens the plain directory name of dir as a root of its own. st is what Lstat said of it: the directory
// that was opened must be the same one, so a name swapped for a symlink in between is refused.
func openDir(dir *os.Root, name string, st fs.FileInfo) (*os.Root, error) {
	sub, err := dir.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	got, err := sub.Stat(".")
	if err != nil {
		sub.Close()
		return nil, err
	}
	if !os.SameFile(st, got) {
		sub.Close()
		return nil, refused("%s changed while it was opened", name)
	}
	return sub, nil
}

// ---- writing a tree in ----

// ExtractOpts are the options of Extract.
type ExtractOpts struct {
	// Preserve sets owner, group and times from the archive. A failed chown is ignored when the process has no right
	// to chown (a user, not root).
	Preserve bool
	Limits   Limits
}

// Extract writes the tar stream r below dst, a root held on the destination directory. Entry names are relative to
// dst.
func Extract(dst *os.Root, r io.Reader, o ExtractOpts) (Stats, error) {
	x := &extractor{dst: dst, o: o}
	defer x.closeCache()
	var src io.Reader = r
	if max := o.Limits.MaxBytes; max > 0 {
		src = &limitReader{r: r, left: max + 1<<20} // the archive's own headers and padding are not data
		x.dataMax = max
	}
	tr := tar.NewReader(src)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tl *tooLarge
			if errors.As(err, &tl) {
				return x.st, fmt.Errorf("%w: more than %d bytes", ErrTooLarge, o.Limits.MaxBytes)
			}
			return x.st, fmt.Errorf("%w: the archive is damaged: %v", ErrRefused, err)
		}
		if err := x.entry(h, tr); err != nil {
			return x.st, err
		}
	}
	return x.st, nil
}

type tooLarge struct{}

func (*tooLarge) Error() string { return "too large" }

// limitReader fails with tooLarge after left bytes.
type limitReader struct {
	r    io.Reader
	left int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		return 0, &tooLarge{}
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

type extractor struct {
	dst     *os.Root
	o       ExtractOpts
	st      Stats
	n       int
	dataMax int64
	// the parent directory of the last entry, held open: archives list the files of a directory together
	lastDir  string
	lastRoot *os.Root
	// swept are the directories whose leftover temporary files were removed
	swept map[string]bool
}

func (x *extractor) closeCache() {
	if x.lastRoot != nil && x.lastRoot != x.dst {
		x.lastRoot.Close()
	}
	x.lastRoot, x.lastDir = nil, ""
}

// checkName refuses a name that is not a relative, clean path of plain segments.
func checkName(name string, depth int) error {
	if name == "" || len(name) > MaxNameBytes || strings.ContainsRune(name, 0) {
		return refused("the name %q is empty, too long or has a NUL byte", abbreviate(name))
	}
	if strings.HasPrefix(name, "/") {
		return refused("the name %q is absolute", abbreviate(name))
	}
	segs := strings.Split(strings.TrimSuffix(name, "/"), "/")
	if len(segs) > depth {
		return refused("the name %q is more than %d directories deep", abbreviate(name), depth)
	}
	for _, s := range segs {
		if s == "" || s == "." || s == ".." {
			return refused("the name %q has an empty, . or .. segment", abbreviate(name))
		}
	}
	return nil
}

func abbreviate(s string) string {
	if len(s) > 80 {
		return s[:80] + "..."
	}
	return s
}

func (x *extractor) entry(h *tar.Header, body io.Reader) error {
	if err := checkName(h.Name, x.o.Limits.depth()); err != nil {
		return err
	}
	if x.n++; x.n > x.o.Limits.entries() {
		return fmt.Errorf("%w: more than %d entries", ErrTooLarge, x.o.Limits.entries())
	}
	name := strings.TrimSuffix(h.Name, "/")
	dir, base := path.Split(name)
	dir = strings.TrimSuffix(dir, "/")
	switch h.Typeflag {
	case tar.TypeDir:
		return x.mkdir(dir, base, h)
	case tar.TypeReg:
		return x.file(dir, base, h, body)
	case tar.TypeSymlink:
		return x.symlink(dir, base, h)
	case tar.TypeLink:
		return refused("%s is a hard link: hard links are not copied", abbreviate(h.Name))
	case tar.TypeXGlobalHeader:
		return nil
	default:
		return refused("%s is a device, FIFO or other special file: only files, directories and symlinks are copied", abbreviate(h.Name))
	}
}

// parent opens the directory dir (relative to dst, "" is dst) one segment at a time, following no symlink, and
// creates the missing segments.
func (x *extractor) parent(dir string) (*os.Root, error) {
	if x.lastRoot != nil && x.lastDir == dir {
		return x.lastRoot, nil
	}
	x.closeCache()
	cur := x.dst
	if dir != "" {
		for _, seg := range strings.Split(dir, "/") {
			st, err := cur.Lstat(seg)
			if errors.Is(err, fs.ErrNotExist) {
				if err = cur.Mkdir(seg, 0o755); err != nil {
					return nil, err
				}
				st, err = cur.Lstat(seg)
			}
			if err != nil {
				return nil, err
			}
			if st.Mode()&fs.ModeSymlink != 0 {
				return nil, refused("%s is a symlink: a symlink is never followed", seg)
			}
			if !st.IsDir() {
				return nil, refused("%s is not a directory", seg)
			}
			next, err := openDir(cur, seg, st)
			if err != nil {
				return nil, err
			}
			if cur != x.dst {
				cur.Close()
			}
			cur = next
		}
	}
	x.lastRoot, x.lastDir = cur, dir
	return cur, nil
}

func (x *extractor) mkdir(dir, base string, h *tar.Header) error {
	p, err := x.parent(dir)
	if err != nil {
		return err
	}
	st, err := p.Lstat(base)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := p.Mkdir(base, 0o755); err != nil {
			return err
		}
		st, err = p.Lstat(base)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	case st.Mode()&fs.ModeSymlink != 0:
		return refused("%s is a symlink: a symlink is never replaced by a directory", abbreviate(h.Name))
	case !st.IsDir():
		return refused("%s exists and is not a directory", abbreviate(h.Name))
	}
	d, err := openDir(p, base, st)
	if err != nil {
		return err
	}
	defer d.Close()
	f, err := d.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := x.apply(f, h, true); err != nil {
		return err
	}
	x.st.Dirs++
	return nil
}

// apply sets mode, and with Preserve owner and times, on the open file or directory f.
// A failed chmod or chown is an error (a file whose owner is not the one asked for must not be reported as written), except
// the chown of a user that is not root, which keeps its own ids.
func (x *extractor) apply(f *os.File, h *tar.Header, dir bool) error {
	mode := fs.FileMode(h.Mode) & 0o777 // never the set-id and sticky bits
	if dir {
		mode |= 0o700
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if x.o.Preserve {
		if err := f.Chown(h.Uid, h.Gid); chownFailed(err) {
			return err
		}
		setTimes(f, h.ModTime)
	}
	return nil
}

// chownFailed reports whether err is a chown failure to return: any, but the refusal of a process that is not root.
func chownFailed(err error) bool {
	return err != nil && !(errors.Is(err, syscall.EPERM) && os.Geteuid() != 0)
}

func (x *extractor) file(dir, base string, h *tar.Header, body io.Reader) error {
	p, err := x.parent(dir)
	if err != nil {
		return err
	}
	if st, err := p.Lstat(base); err == nil && st.IsDir() {
		return refused("%s is a directory: a directory is never replaced by a file", abbreviate(h.Name))
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if max := x.o.Limits.MaxBytes; max > 0 && x.st.Bytes+h.Size > max {
		return fmt.Errorf("%w: more than %d bytes", ErrTooLarge, max)
	}
	x.sweep(p, dir)
	tmp, f, err := x.temp(p)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		f.Close()
		_ = p.Remove(tmp)
		return err
	}
	n, err := io.Copy(f, body)
	x.st.Bytes += n
	if err != nil {
		var tl *tooLarge
		if errors.As(err, &tl) {
			err = fmt.Errorf("%w: more than %d bytes", ErrTooLarge, x.o.Limits.MaxBytes)
		}
		return fail(err)
	}
	if n != h.Size {
		return fail(refused("%s is shorter than its header says", abbreviate(h.Name)))
	}
	if err := x.apply(f, h, false); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = p.Remove(tmp)
		return err
	}
	if err := p.Rename(tmp, base); err != nil {
		_ = p.Remove(tmp)
		return err
	}
	x.st.Files++
	return nil
}

// tempRE is the name of the temporary file of a write: the process id of the writer, a time and a counter.
var tempRE = regexp.MustCompile(`^\.jailops-cp-([0-9]+)-[0-9]+-[0-9]+$`)

// sweep removes what a writer left in dir when it was killed (SIGKILL, a power loss): a regular file whose name is a
// temporary name of a process that no longer exists. It runs once per directory and writer, before the first file of
// the directory is written, and touches nothing else: not a file of another name, not one of a live writer. A leftover
// is 0600 and owned by the writer, never wider than the file it would have become.
func (x *extractor) sweep(p *os.Root, dir string) {
	if x.swept[dir] {
		return
	}
	if x.swept == nil {
		x.swept = map[string]bool{}
	}
	x.swept[dir] = true
	names, err := readNames(p)
	if err != nil {
		return
	}
	for _, n := range names {
		m := tempRE.FindStringSubmatch(n)
		if m == nil {
			continue
		}
		pid, err := strconv.Atoi(m[1])
		if err != nil || pid <= 0 || processAlive(pid) {
			continue
		}
		if st, err := p.Lstat(n); err == nil && st.Mode().IsRegular() {
			_ = p.Remove(n)
		}
	}
}

// processAlive says whether a process with this id exists (kill with signal 0 sends nothing).
func processAlive(pid int) bool {
	if pid == os.Getpid() {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// temp creates the temporary file a regular file is written to: exclusive, 0600, never through a link.
func (x *extractor) temp(p *os.Root) (string, *os.File, error) {
	for i := 0; i < 100; i++ {
		name := fmt.Sprintf(".jailops-cp-%d-%d-%d", os.Getpid(), time.Now().UnixNano(), i)
		f, err := p.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
		if err == nil {
			return name, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, err
		}
	}
	return "", nil, errors.New("no free temporary name")
}

func (x *extractor) symlink(dir, base string, h *tar.Header) error {
	if strings.ContainsRune(h.Linkname, 0) || len(h.Linkname) > MaxNameBytes {
		return refused("the target of symlink %s is not usable", abbreviate(h.Name))
	}
	p, err := x.parent(dir)
	if err != nil {
		return err
	}
	if st, err := p.Lstat(base); err == nil {
		if st.IsDir() {
			return refused("%s is a directory: a directory is never replaced by a symlink", abbreviate(h.Name))
		}
		if err := p.Remove(base); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := p.Symlink(h.Linkname, base); err != nil {
		return err
	}
	if x.o.Preserve {
		if err := p.Lchown(base, h.Uid, h.Gid); chownFailed(err) {
			return err
		}
	}
	x.st.Symlinks++
	return nil
}
