package priv

import (
	"bufio"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"syscall"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/tarcopy"
)

// Files of a jail root: fs.jail_stat, fs.jail_get and fs.jail_put (`jailops cp`, the file routes of the API).
//
// The helper takes the jail and its root path (checkJailRoot: <storage mount>/projects/<project>/jails/<name>/root
// of an active storage root, reached without a symlink) and a path below it. Everything below the root is reached
// through an os.Root that cannot leave it, one directory at a time, following no symlink (openBelow); the transfer
// itself is sdk/tarcopy, whose rules are listed there: no ".." or absolute names, no devices or hard links, no
// set-id bits, files written to a temporary name and renamed, a bounded number of bytes and entries.
//
// Nothing of this is a shell and nothing runs in the jail: a jail that is not mounted cannot be reached, and the
// answer is busy. The helper does not decide who may copy: that is the role of the caller, as for exec.
// UNVERIFIED: on a FreeBSD host with mounted jail roots; the confinement is tested against real directories,
// symlinks and swapped names on the development host.

// maxCopyEntries bounds the entries of one transfer.
const maxCopyEntries = tarcopy.DefaultMaxEntries

// acquireCopy takes a transfer slot; the returned function frees it.
func (l *Local) acquireCopy() (func(), error) {
	if int(l.copies.Add(1)) > l.cfg.Limits.MaxJailCopies {
		l.copies.Add(-1)
		return nil, api.Errorf(api.CodeBusy, "too many file transfers at once (limit %d)", l.cfg.Limits.MaxJailCopies)
	}
	return func() { l.copies.Add(-1) }, nil
}

// copyLimit is the byte limit of a transfer: the caller's, never more than the helper's.
func (l *Local) copyLimit(asked int64) int64 {
	max := l.cfg.Limits.MaxJailCopyBytes
	if asked > 0 && asked < max {
		return asked
	}
	return max
}

// jailRootOf opens the root of a jail as an os.Root. A root that is missing or empty (its dataset is not mounted)
// is not_found with the detail missing=root, like the log directory.
func (l *Local) jailRootOf(jail, root string) (*os.Root, error) {
	if err := l.cfg.checkJailRoot(jail, root); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, missing("root", "jail root %s is missing", root)
		}
		return nil, mapFSErr(err)
	}
	top, err := r.Open(".")
	if err != nil {
		r.Close()
		return nil, mapFSErr(err)
	}
	names, err := top.Readdirnames(1)
	top.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		r.Close()
		return nil, mapFSErr(err)
	}
	if len(names) == 0 {
		r.Close()
		return nil, missing("root", "jail root %s is empty", root)
	}
	return r, nil
}

// relSegments splits a path below a jail root; "" is the root itself.
func relSegments(p string) ([]string, error) {
	c, err := api.CleanJailFilePath(p)
	if err != nil {
		return nil, err
	}
	if c != p {
		return nil, invalid("the path %q is not clean: no leading or trailing slash", p)
	}
	if c == "" {
		return nil, nil
	}
	return strings.Split(c, "/"), nil
}

// parentOf opens the directory that holds the last segment of segs: every segment before it must be a plain
// directory. With mkdir the missing ones are created. The root itself is returned (not a copy) for one segment.
func parentOf(jr *os.Root, segs []string, mkdir bool) (*os.Root, error) {
	cur := jr
	closeCur := func() {
		if cur != jr {
			cur.Close()
		}
	}
	for _, seg := range segs {
		st, err := cur.Lstat(seg)
		created := false
		if errors.Is(err, fs.ErrNotExist) && mkdir {
			if err = cur.Mkdir(seg, 0o755); err == nil {
				created = true
				st, err = cur.Lstat(seg)
			}
		}
		if err != nil {
			closeCur()
			return nil, err
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			closeCur()
			return nil, forbidden("%s is a symlink: a symlink is never followed", seg)
		}
		if !st.IsDir() {
			closeCur()
			return nil, api.Errorf(api.CodeInvalid, "%s is not a directory", seg)
		}
		next, err := cur.OpenRoot(seg)
		if err != nil {
			closeCur()
			return nil, err
		}
		got, err := next.Stat(".")
		if err != nil || !os.SameFile(st, got) {
			next.Close()
			closeCur()
			if err != nil {
				return nil, err
			}
			return nil, forbidden("%s changed while it was opened", seg)
		}
		if created {
			if err := rootWheel(next); err != nil {
				next.Close()
				closeCur()
				return nil, err
			}
		}
		closeCur()
		cur = next
	}
	return cur, nil
}

// rootWheel gives a directory made for a file or directory below it the rights of a system directory: root:wheel 0755,
// whatever the umask or the group of its parent says, and never the owner of the file that follows. A process that is
// not root keeps its own ids.
func rootWheel(d *os.Root) error {
	f, err := d.Open(".")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Chmod(0o755); err != nil {
		return err
	}
	if err := f.Chown(0, 0); err != nil && !(errors.Is(err, syscall.EPERM) && os.Geteuid() != 0) {
		return err
	}
	return nil
}

// copyErr maps what tarcopy and the file system answer to the helper's errors.
func copyErr(err error) error {
	var ae *api.Error
	switch {
	case err == nil:
		return nil
	case errors.As(err, &ae):
		return err
	case errors.Is(err, tarcopy.ErrRefused):
		return forbidden("%v", trimRefused(err))
	case errors.Is(err, tarcopy.ErrTooLarge):
		return api.Errorf(api.CodeInvalid, "%v", trimTooLarge(err)).WithHint("copy less in one go, or in pieces")
	case errors.Is(err, syscall.ENOSPC):
		return api.Errorf(api.CodeBusy, "the jail has no space left").WithHint("free space in the jail or raise its disk limit")
	}
	return mapFSErr(err)
}

func trimRefused(err error) string {
	return strings.TrimPrefix(err.Error(), tarcopy.ErrRefused.Error()+": ")
}

func trimTooLarge(err error) string {
	return strings.TrimPrefix(err.Error(), tarcopy.ErrTooLarge.Error()+": ")
}

// JailStat implements Host: what is at a path of a jail root.
func (l *Local) JailStat(ctx context.Context, q service.JailStatReq) (api.JailFileStat, error) {
	segs, err := relSegments(q.Path)
	if err != nil {
		return api.JailFileStat{}, err
	}
	jr, err := l.jailRootOf(q.Jail, q.Root)
	if err != nil {
		return api.JailFileStat{}, err
	}
	defer jr.Close()
	out := api.JailFileStat{Path: "/" + q.Path}
	if len(segs) == 0 {
		out.Type = api.FileTypeDir
		return out, nil
	}
	p, err := parentOf(jr, segs[:len(segs)-1], false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			out.Type = api.FileTypeNone
			return out, nil
		}
		return out, copyErr(err)
	}
	if p != jr {
		defer p.Close()
	}
	st, err := p.Lstat(segs[len(segs)-1])
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			out.Type = api.FileTypeNone
			return out, nil
		}
		return out, copyErr(err)
	}
	mt := st.ModTime().UTC()
	out.ModifiedAt = &mt
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		out.Type = api.FileTypeSymlink
		out.Link, _ = p.Readlink(segs[len(segs)-1])
	case st.IsDir():
		out.Type = api.FileTypeDir
	case st.Mode().IsRegular():
		out.Type = api.FileTypeFile
		out.Size = st.Size()
	default:
		out.Type = api.FileTypeOther
	}
	out.Mode = octal(st.Mode().Perm())
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		out.Uid, out.Gid = int(sys.Uid), int(sys.Gid)
	}
	return out, nil
}

// JailList implements Host: the entries of a directory of a jail root, by name, at most api.MaxFileListEntries. Every
// segment of the path is a plain directory (a symlink is refused, never followed); nothing is stat-ed per entry.
func (l *Local) JailList(ctx context.Context, q service.JailStatReq) (api.JailFileList, error) {
	segs, err := relSegments(q.Path)
	if err != nil {
		return api.JailFileList{}, err
	}
	jr, err := l.jailRootOf(q.Jail, q.Root)
	if err != nil {
		return api.JailFileList{}, err
	}
	defer jr.Close()
	dir, err := parentOf(jr, segs, false)
	if err != nil {
		return api.JailFileList{}, copyErr(err)
	}
	if dir != jr {
		defer dir.Close()
	}
	f, err := dir.Open(".")
	if err != nil {
		return api.JailFileList{}, copyErr(err)
	}
	defer f.Close()
	es, err := f.ReadDir(api.MaxFileListEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return api.JailFileList{}, copyErr(err)
	}
	out := api.JailFileList{Path: "/" + q.Path, Entries: []api.JailFileEntry{}}
	if len(es) > api.MaxFileListEntries {
		es, out.Truncated = es[:api.MaxFileListEntries], true
	}
	for _, e := range es {
		out.Entries = append(out.Entries, api.JailFileEntry{Name: e.Name(), Type: entryType(e.Type())})
	}
	sort.Slice(out.Entries, func(i, j int) bool { return out.Entries[i].Name < out.Entries[j].Name })
	return out, nil
}

// entryType names the type bits of a directory entry.
func entryType(m fs.FileMode) api.JailFileType {
	switch {
	case m&fs.ModeSymlink != 0:
		return api.FileTypeSymlink
	case m.IsDir():
		return api.FileTypeDir
	case m.IsRegular():
		return api.FileTypeFile
	}
	return api.FileTypeOther
}

func octal(m fs.FileMode) string {
	const digits = "01234567"
	v := uint32(m)
	return "0" + string([]byte{digits[v>>6&7], digits[v>>3&7], digits[v&7]})
}

// JailGet implements Host: the file, symlink or tree at a path of a jail root as a tar stream.
func (l *Local) JailGet(ctx context.Context, q service.JailGetReq, out io.Writer) (tarcopy.Stats, error) {
	segs, err := relSegments(q.Path)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	if len(segs) == 0 {
		return tarcopy.Stats{}, api.Errorf(api.CodeInvalid, "name a file or a directory of the jail, not its root").WithHint("copy its directories one by one")
	}
	if q.As != "" && strings.ContainsAny(q.As, "/\x00") {
		return tarcopy.Stats{}, invalid("the name %q must be one name", q.As)
	}
	done, err := l.acquireCopy()
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer done()
	jr, err := l.jailRootOf(q.Jail, q.Root)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer jr.Close()
	p, err := parentOf(jr, segs[:len(segs)-1], false)
	if err != nil {
		return tarcopy.Stats{}, copyErr(err)
	}
	if p != jr {
		defer p.Close()
	}
	w := bufio.NewWriterSize(out, ChunkSize)
	st, err := tarcopy.Create(p, segs[len(segs)-1], q.As, w, tarcopy.CreateOpts{
		Recursive: q.Recursive, Preserve: q.Preserve,
		Limits: tarcopy.Limits{MaxBytes: l.copyLimit(q.MaxBytes), MaxEntries: maxCopyEntries},
	})
	if ferr := w.Flush(); err == nil {
		err = ferr
	}
	return st, copyErr(err)
}

// JailPut implements Host: a tar stream written below a directory of a jail root.
func (l *Local) JailPut(ctx context.Context, q service.JailPutReq, in io.Reader) (tarcopy.Stats, error) {
	segs, err := relSegments(q.Path)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	done, err := l.acquireCopy()
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer done()
	jr, err := l.jailRootOf(q.Jail, q.Root)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer jr.Close()
	dir, err := parentOf(jr, segs, q.Mkdir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return tarcopy.Stats{}, api.Errorf(api.CodeNotFound, "the directory /%s does not exist in the jail", q.Path).
				WithDetail("missing", "file").WithHint("create it, or copy with mkdir")
		}
		return tarcopy.Stats{}, copyErr(err)
	}
	if dir != jr {
		defer dir.Close()
	}
	st, err := tarcopy.Extract(dir, in, tarcopy.ExtractOpts{
		Preserve: q.Preserve,
		Limits:   tarcopy.Limits{MaxBytes: l.copyLimit(q.MaxBytes), MaxEntries: maxCopyEntries},
	})
	return st, copyErr(err)
}

// ---- over the socket ----

// copyRes is the reply of fs.jail_get and fs.jail_put.
type copyRes struct {
	tarcopy.Stats
}

func init() {
	register(OpFSJailStat, false, func(ctx context.Context, h Host, a service.JailStatReq, _ *callIO) (any, error) {
		return h.JailStat(ctx, a)
	})
	register(OpFSJailList, false, func(ctx context.Context, h Host, a service.JailStatReq, _ *callIO) (any, error) {
		return h.JailList(ctx, a)
	})
	register(OpFSJailGet, true, func(ctx context.Context, h Host, a service.JailGetReq, c *callIO) (any, error) {
		st, err := h.JailGet(ctx, a, c.Out)
		return copyRes{st}, err
	})
	register(OpFSJailPut, true, func(ctx context.Context, h Host, a service.JailPutReq, c *callIO) (any, error) {
		st, err := h.JailPut(ctx, a, c.In)
		return copyRes{st}, err
	})
}

// JailStat implements Host.
func (c *Client) JailStat(ctx context.Context, q service.JailStatReq) (api.JailFileStat, error) {
	return callJSON[api.JailFileStat](ctx, c, OpFSJailStat, q, callOpts{})
}

// JailList implements Host.
func (c *Client) JailList(ctx context.Context, q service.JailStatReq) (api.JailFileList, error) {
	return callJSON[api.JailFileList](ctx, c, OpFSJailList, q, callOpts{})
}

// JailGet implements Host.
func (c *Client) JailGet(ctx context.Context, q service.JailGetReq, out io.Writer) (tarcopy.Stats, error) {
	r, err := callJSON[copyRes](ctx, c, OpFSJailGet, q, callOpts{out: out})
	return r.Stats, err
}

// JailPut implements Host.
func (c *Client) JailPut(ctx context.Context, q service.JailPutReq, in io.Reader) (tarcopy.Stats, error) {
	r, err := callJSON[copyRes](ctx, c, OpFSJailPut, q, callOpts{in: in, fill: true})
	return r.Stats, err
}
