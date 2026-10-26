package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"

	"jailops.net/jailops/internal/repo/chunker"
)

// ScanOptions tune the walk.
type ScanOptions struct {
	// Prev is the catalog of the previous snapshot (nil = nothing to reuse).
	Prev *Tree
	// TrustCtimeInode also requires equal ctime and inode number for a file to count as unchanged.
	// On successive snapshots of one ZFS dataset an untouched file keeps both, and ctime cannot be
	// set from user space, which closes the hole of "size and mtime preserved, content changed".
	// Switch it off for sources where they are not stable (copies staged anew every run).
	TrustCtimeInode bool
	// NoReuse forces every file to be read (full-data mode).
	NoReuse bool
	// Pack is the packing rule for small files (zero fields take the format
	// defaults). Boundaries depend only on names and sizes, so adding or
	// removing a file disturbs the packs up to the next name-defined boundary.
	Pack chunker.PackPolicy
	// Attrs collects extended attributes and ACLs (nil = the platform's). It is used by the local FS;
	// a remote FS collects them itself.
	Attrs Attrs
	// FS is the tree to read (nil = the local file system). An unprivileged caller reads a snapshot
	// through an agent that holds the privileges (internal/fsagent).
	FS ScanFS
	// Workers is how many entries are examined concurrently (0 = DefaultScanWorkers). The result
	// does not depend on it.
	Workers int
	// OnRun, when set, receives every run as soon as the assembly has laid it out (in payload
	// order) instead of collecting them in Result.Runs: the caller reads and encodes a run while the
	// walk goes on, so the run layout of a whole tree (a path per file) is never held at once. An
	// error from OnRun ends the scan with that error.
	OnRun func(*Run) error
	// Ahead bounds how many entries the concurrent walk may have examined beyond the assembly
	// (0 = DefaultScanAhead), so a slow consumer (OnRun reading files) does not make the walk hold
	// the state of the whole tree. The result does not depend on it.
	Ahead int
}

// DefaultScanAhead is how many examined but not yet assembled entries the walk may hold.
const DefaultScanAhead = 64 << 10

// parkedTotal counts parked listings (tests check the bound is really exercised).
var parkedTotal atomic.Int64

func (o *ScanOptions) norm() {
	o.Pack = o.Pack.Norm()
	if o.Attrs == nil {
		o.Attrs = DefaultAttrs()
	}
	if o.FS == nil {
		o.FS = LocalFS(o.Attrs)
	}
}

// RunFile is one regular file inside a run.
type RunFile struct {
	Rel   string // path inside the tree
	Path  string // path on disk
	Entry *Entry
}

// Run is a stretch of the payload that starts and ends on a chunk boundary: either one large
// file or a pack of small ones. Chunk boundaries never cross a run, so a run whose files are
// unchanged maps onto whole chunks of the previous snapshot and can be reused without reading.
type Run struct {
	Start, End uint64
	Large      bool
	Files      []RunFile
	// Reusable: every file matches the previous catalog and the old bytes are contiguous in the
	// old payload at [OldStart, OldEnd). The caller still has to confirm that the old chunk index
	// has boundaries there.
	Reusable         bool
	OldStart, OldEnd uint64
}

// Result of a scan.
type Result struct {
	Tree        *Tree
	Runs        []Run // empty when ScanOptions.OnRun is set
	PayloadSize uint64
	// Counts
	Files, Dirs, Other int
}

// metadata equality: everything the catalog stores except where the bytes live.
func same(prev, cur *Entry, trust bool) bool {
	if prev == nil || prev.Kind != cur.Kind || prev.Mode != cur.Mode || prev.UID != cur.UID || prev.GID != cur.GID ||
		prev.MtimeNs != cur.MtimeNs || prev.Size != cur.Size || prev.Link != cur.Link || prev.Flags != cur.Flags ||
		prev.Major != cur.Major || prev.Minor != cur.Minor || !attrsEqual(prev.Attrs, cur.Attrs) {
		return false
	}
	if trust && (prev.CtimeNs != cur.CtimeNs || prev.Inode != cur.Inode) {
		return false
	}
	return true
}

// DefaultScanWorkers is how many goroutines stat entries and read their xattrs concurrently.
// The walk is syscall bound (lstat, llistxattr, readlink, getdents); on a cold cache every call can
// wait for the disk, so the concurrency is independent of the core count.
const DefaultScanWorkers = 16

// statBatch is how many entries of one directory a worker handles in one job.
const statBatch = 64

// Scan walks root (a read-only snapshot mount) and builds the catalog and the run layout. It
// reads no file content.
//
// The walk has two halves. Workers list directories and lstat every entry (plus xattrs and
// symlink targets) concurrently. The assembly walks the tree in pre-order with directory entries
// sorted by name, waiting for each directory's results, and does everything order dependent there,
// alone: hard-link detection, payload offsets, packs, comparison with the previous catalog and the
// first error to report. The result is therefore exactly what a sequential walk produces.
func Scan(root string, o ScanOptions) (*Result, error) {
	o.norm()
	s := &scanner{o: o, root: root, res: &Result{}, seen: map[[2]uint64]string{}}
	s.prevLimit = prevPayloadLimit(o.Prev)
	var prevRoot *Entry
	if o.Prev != nil && !o.NoReuse {
		prevRoot = o.Prev.Root
	}
	rn := &snode{path: root, rootJoin: true}
	rn.e, rn.nlink, rn.dev, rn.err = o.FS.StatEntry(root, "")
	if rn.err != nil {
		return nil, rn.err
	}
	if rn.e.Kind != KDir {
		return nil, fmt.Errorf("%s is not a directory", root)
	}
	w := newWalk(o.Workers)
	w.limit = int64(o.Ahead)
	if w.limit <= 0 {
		w.limit = DefaultScanAhead
	}
	w.fs = o.FS
	s.w = w
	w.listDir(rn)
	w.start()
	defer w.stop()
	if err := s.dir(rn, "", prevRoot); err != nil {
		return nil, err
	}
	s.closePack()
	if s.err != nil {
		return nil, s.err
	}
	s.res.Tree = &Tree{Root: rn.e, Count: s.count + 1}
	s.res.PayloadSize = s.off
	return s.res, nil
}

// snode is one entry while the tree is being walked.
type snode struct {
	path  string
	name  string
	e     *Entry
	nlink uint64
	dev   uint64
	err   error // stat, attribute or readlink failure (ErrSocket for a socket)
	// rootJoin: the children's paths are built with filepath.Join (the root as given may be unclean)
	rootJoin bool
	// directories: listing results; done is closed once kids are listed and every kid is stat'ed
	kids    []*snode
	listErr error
	pending atomic.Int32
	done    chan struct{}
	// listing throttle (walk.mu): parked = the listing waits for the assembly to catch up; urgent =
	// the assembly waits for this directory, so its listing never parks
	parked, urgent bool
	listJob        func()
}

// statEntry reads everything the catalog stores about one entry, except hard-link identity
// (decided by the in-order assembly).
func statEntry(path, name string, attrs Attrs) (*Entry, uint64, uint64, error) {
	si, err := lstat(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	e := &Entry{
		Name: name, Mode: si.Perm, UID: si.UID, GID: si.GID,
		MtimeNs: si.Mtime, CtimeNs: si.Ctime, Inode: si.Ino, Flags: si.Flags,
	}
	if err := kindOf(path, si, e); err != nil {
		return nil, 0, 0, err
	}
	x, err := attrs.Collect(path)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("reading attributes of %s: %w", path, err)
	}
	e.Attrs = x
	return e, si.Nlink, si.Dev, nil
}

// joinPath is filepath.Join for a clean dir and a plain name, without the cost of cleaning.
func joinPath(dir, name string) string {
	if dir == "" || dir[len(dir)-1] == '/' {
		return filepath.Join(dir, name)
	}
	return dir + "/" + name
}

// walk is the concurrent half: a LIFO of jobs (depth first, so results arrive roughly in the
// order the assembly consumes them) served by a fixed set of workers.
type walk struct {
	fs      ScanFS
	mu      sync.Mutex
	cond    *sync.Cond
	jobs    []func()
	closed  bool
	workers int
	wg      sync.WaitGroup
	// ahead counts entries listed but not yet consumed by the assembly. A directory listing that
	// would start while ahead > limit is parked until the assembly catches up (or needs it): only
	// listings create entries, so this bounds the walk's state without ever blocking a worker.
	ahead   atomic.Int64
	limit   int64
	parked  []*snode
	nparked atomic.Int32 // len(parked), readable without the lock
}

func newWalk(n int) *walk {
	if n <= 0 {
		n = DefaultScanWorkers
	}
	w := &walk{workers: n}
	w.cond = sync.NewCond(&w.mu)
	return w
}

func (w *walk) start() {
	for i := 0; i < w.workers; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for {
				w.mu.Lock()
				for len(w.jobs) == 0 && !w.closed {
					w.cond.Wait()
				}
				if w.closed {
					w.mu.Unlock()
					return
				}
				j := w.jobs[len(w.jobs)-1]
				w.jobs[len(w.jobs)-1] = nil
				w.jobs = w.jobs[:len(w.jobs)-1]
				w.mu.Unlock()
				j()
			}
		}()
	}
}

// stop ends the workers (pending jobs are dropped: the assembly no longer needs them).
func (w *walk) stop() {
	w.mu.Lock()
	w.closed = true
	w.jobs = nil
	w.cond.Broadcast()
	w.mu.Unlock()
	w.wg.Wait()
}

func (w *walk) push(j func()) {
	w.mu.Lock()
	w.jobs = append(w.jobs, j)
	w.mu.Unlock()
	w.cond.Signal()
}

// need is called by the assembly before it waits for a directory: a parked listing of it is
// queued at once, and it will not park later.
func (w *walk) need(n *snode) {
	w.mu.Lock()
	n.urgent = true
	if n.parked {
		n.parked = false
		for i, p := range w.parked {
			if p == n {
				w.parked = append(w.parked[:i], w.parked[i+1:]...)
				w.nparked.Store(int32(len(w.parked)))
				break
			}
		}
		w.jobs = append(w.jobs, n.listJob)
	}
	w.mu.Unlock()
	w.cond.Broadcast()
}

// consumed is called by the assembly for every entry it has taken over; once the walk is well
// within its bound again, the parked listings are released.
func (w *walk) consumed() {
	if w.ahead.Add(-1) > w.limit/2 || w.nparked.Load() == 0 {
		return
	}
	w.mu.Lock()
	if len(w.parked) == 0 {
		w.mu.Unlock()
		return
	}
	for _, p := range w.parked {
		p.parked = false
		w.jobs = append(w.jobs, p.listJob)
	}
	w.parked = nil
	w.nparked.Store(0)
	w.mu.Unlock()
	w.cond.Broadcast()
}

// listDir queues the listing of a directory node.
func (w *walk) listDir(n *snode) {
	n.done = make(chan struct{})
	var job func()
	job = func() {
		w.mu.Lock()
		if !n.urgent && !w.closed && w.ahead.Load() > w.limit {
			n.parked = true
			parkedTotal.Add(1)
			w.parked = append(w.parked, n)
			w.nparked.Store(int32(len(w.parked)))
			w.mu.Unlock()
			return
		}
		w.mu.Unlock()
		names, err := w.fs.ReadNames(n.path)
		if err != nil {
			n.listErr = fmt.Errorf("reading %s: %w", n.path, err)
			close(n.done)
			return
		}
		sort.Strings(names)
		w.ahead.Add(int64(len(names)))
		n.kids = make([]*snode, len(names))
		for i, name := range names {
			p := filepath.Join(n.path, name)
			if !n.rootJoin {
				p = joinPath(n.path, name)
			}
			n.kids[i] = &snode{name: name, path: p}
		}
		if len(names) == 0 {
			close(n.done)
			return
		}
		nb := (len(names) + statBatch - 1) / statBatch
		n.pending.Store(int32(nb))
		for b := 0; b < nb; b++ {
			kids := n.kids[b*statBatch : min((b+1)*statBatch, len(names))]
			w.push(func() {
				for _, k := range kids {
					k.e, k.nlink, k.dev, k.err = w.fs.StatEntry(k.path, k.name)
					if k.err == nil && k.e.Kind == KDir {
						w.listDir(k)
					}
				}
				if n.pending.Add(-1) == 0 {
					close(n.done)
				}
			})
		}
	}
	n.listJob = job
	w.push(job)
}

type scanner struct {
	o     ScanOptions
	root  string
	res   *Result
	seen  map[[2]uint64]string // (dev, ino) -> first path, for hard links
	off   uint64
	pack  *Run
	count int
	w     *walk
	err   error // first error of OnRun
	// prevLimit is the end of the previous payload: a catalog that points outside its own
	// payload is not trusted for reuse.
	prevLimit uint64
}

// dir assembles a directory whose node was queued for listing: in name order, exactly as a
// sequential walk would.
func (s *scanner) dir(n *snode, rel string, prev *Entry) error {
	s.w.need(n)
	<-n.done
	if n.listErr != nil {
		return n.listErr
	}
	e := n.e
	kids := n.kids
	n.kids = nil // let the walk state go as soon as it is assembled
	for i, k := range kids {
		kids[i] = nil
		s.w.consumed()
		if s.err != nil {
			return s.err
		}
		crel := k.name
		if rel != "" {
			crel = rel + "/" + k.name
		}
		var pc *Entry
		if prev != nil && prev.Kind == KDir {
			pc = prev.child(k.name)
		}
		if errors.Is(k.err, ErrSocket) {
			continue // sockets cannot be archived or restored
		}
		if k.err != nil {
			return k.err
		}
		c := k.e
		if c.Kind == KFile && k.nlink > 1 {
			key := [2]uint64{k.dev, c.Inode}
			if first, ok := s.seen[key]; ok {
				c.Kind, c.Size, c.Link = KHardlink, 0, first
			} else {
				s.seen[key] = crel
			}
		}
		e.Children = append(e.Children, c)
		s.count++
		switch c.Kind {
		case KDir:
			s.res.Dirs++
			if err := s.dir(k, crel, pc); err != nil {
				return err
			}
		case KFile:
			s.res.Files++
			if c.Size > 0 {
				s.addFile(RunFile{Rel: crel, Path: k.path, Entry: c}, pc)
			}
		default:
			s.res.Other++
		}
	}
	return nil
}

// addFile places a file with content into the run layout.
func (s *scanner) addFile(f RunFile, prev *Entry) {
	size := int64(f.Entry.Size)
	reusable := prev != nil && !s.o.NoReuse && prev.Size > 0 && same(prev, f.Entry, s.o.TrustCtimeInode) && prev.PayOff+prev.Size <= s.prevLimit
	if reusable {
		f.Entry.Sum = prev.Sum
	}
	if s.o.Pack.Large(size) {
		s.closePack()
		r := Run{Start: s.off, End: s.off + f.Entry.Size, Large: true, Files: []RunFile{f}}
		f.Entry.PayOff = s.off
		if reusable {
			r.Reusable, r.OldStart, r.OldEnd = true, prev.PayOff, prev.PayOff+prev.Size
		}
		s.off = r.End
		s.emit(r)
		return
	}
	if s.pack == nil {
		s.pack = &Run{Start: s.off, End: s.off, Reusable: true}
	}
	p := s.pack
	f.Entry.PayOff = s.off
	if p.Reusable {
		switch {
		case !reusable:
			p.Reusable = false
		case len(p.Files) == 0:
			p.OldStart, p.OldEnd = prev.PayOff, prev.PayOff+prev.Size
		case prev.PayOff == p.OldEnd:
			p.OldEnd = prev.PayOff + prev.Size
		default:
			p.Reusable = false
		}
	}
	p.Files = append(p.Files, f)
	s.off += f.Entry.Size
	p.End = s.off
	if s.o.Pack.EndsPack(int64(p.End-p.Start), f.Rel) {
		s.closePack()
	}
}

func (s *scanner) closePack() {
	if s.pack == nil {
		return
	}
	r := *s.pack
	s.pack = nil
	s.emit(r)
}

// emit hands a finished run to OnRun, or collects it.
func (s *scanner) emit(r Run) {
	if s.o.OnRun == nil {
		s.res.Runs = append(s.res.Runs, r)
		return
	}
	if s.err == nil {
		s.err = s.o.OnRun(&r)
	}
}

// prevPayloadLimit is an upper bound for a payload offset in the previous catalog: the end of its
// last file. Used to refuse a catalog that points outside its own payload.
func prevPayloadLimit(t *Tree) uint64 {
	if t == nil {
		return 0
	}
	var m uint64
	var rec func(e *Entry)
	rec = func(e *Entry) {
		if e.Kind == KFile && e.Size > 0 && e.PayOff+e.Size > m {
			m = e.PayOff + e.Size
		}
		for _, c := range e.Children {
			rec(c)
		}
	}
	rec(t.Root)
	return m
}
