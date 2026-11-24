package dtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
	"jailops.net/jailops/sdk/tarcopy"
)

// dirJailFiles is the file access of the test daemon: the jail roots are directories below one temporary directory
// (the helper's os.Root work, without its path class checks). A root with nothing in it is a dataset that is not
// mounted.
type dirJailFiles struct{ base string }

func (d dirJailFiles) open(root string) (*os.Root, error) {
	dir := filepath.Join(d.base, filepath.FromSlash(root))
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, api.Errorf(api.CodeNotFound, "jail root %s is missing", root).WithDetail("missing", "root")
	}
	if es, _ := os.ReadDir(dir); len(es) == 0 {
		r.Close()
		return nil, api.Errorf(api.CodeNotFound, "jail root %s is empty", root).WithDetail("missing", "root")
	}
	return r, nil
}

// JailRootDir is the directory of a jail root of the test daemon (what a test fills to give the jail files).
func (e *Env) JailRootDir(project, jail string) string {
	return filepath.Join(e.jailFilesBase(), "jailops", "projects", project, "jails", jail, "root")
}

func (e *Env) jailFilesBase() string { return filepath.Join(e.Dir, "jailroots") }

func (d dirJailFiles) parent(r *os.Root, p string, mkdir bool) (*os.Root, error) {
	cur := r
	if p == "" {
		return cur, nil
	}
	for _, seg := range strings.Split(p, "/") {
		st, err := cur.Lstat(seg)
		if errors.Is(err, fs.ErrNotExist) && mkdir {
			if err = cur.Mkdir(seg, 0o755); err == nil {
				st, err = cur.Lstat(seg)
			}
		}
		if err != nil {
			return nil, api.Errorf(api.CodeNotFound, "%s does not exist", seg).WithDetail("missing", "file")
		}
		if st.Mode()&fs.ModeSymlink != 0 {
			return nil, api.Errorf(api.CodeForbidden, "%s is a symlink: a symlink is never followed", seg)
		}
		if !st.IsDir() {
			return nil, api.Errorf(api.CodeInvalid, "%s is not a directory", seg)
		}
		if cur, err = cur.OpenRoot(seg); err != nil {
			return nil, err
		}
	}
	return cur, nil
}

func (d dirJailFiles) JailStat(_ context.Context, q service.JailStatReq) (api.JailFileStat, error) {
	r, err := d.open(q.Root)
	if err != nil {
		return api.JailFileStat{}, err
	}
	defer r.Close()
	out := api.JailFileStat{Path: "/" + q.Path, Type: api.FileTypeDir}
	if q.Path == "" {
		return out, nil
	}
	dir, base := filepath.Split(q.Path)
	p, err := d.parent(r, strings.TrimSuffix(dir, "/"), false)
	if err != nil {
		out.Type = api.FileTypeNone
		return out, nil
	}
	st, err := p.Lstat(base)
	if err != nil {
		out.Type = api.FileTypeNone
		return out, nil
	}
	mt := st.ModTime().UTC()
	out.ModifiedAt = &mt
	out.Mode = fmt.Sprintf("%04o", st.Mode().Perm())
	if sys, ok := st.Sys().(*syscall.Stat_t); ok {
		out.Uid, out.Gid = int(sys.Uid), int(sys.Gid)
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		out.Type = api.FileTypeSymlink
		out.Link, _ = p.Readlink(base)
	case st.IsDir():
		out.Type = api.FileTypeDir
	case st.Mode().IsRegular():
		out.Type, out.Size = api.FileTypeFile, st.Size()
	default:
		out.Type = api.FileTypeOther
	}
	return out, nil
}

func (d dirJailFiles) JailGet(_ context.Context, q service.JailGetReq, out io.Writer) (tarcopy.Stats, error) {
	r, err := d.open(q.Root)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer r.Close()
	dir, base := filepath.Split(q.Path)
	p, err := d.parent(r, strings.TrimSuffix(dir, "/"), false)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	st, err := tarcopy.Create(p, base, q.As, out, tarcopy.CreateOpts{Recursive: q.Recursive, Preserve: q.Preserve, Limits: tarcopy.Limits{MaxBytes: q.MaxBytes}})
	return st, mapTar(err)
}

func (d dirJailFiles) JailPut(_ context.Context, q service.JailPutReq, in io.Reader) (tarcopy.Stats, error) {
	r, err := d.open(q.Root)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	defer r.Close()
	p, err := d.parent(r, q.Path, q.Mkdir)
	if err != nil {
		return tarcopy.Stats{}, err
	}
	st, err := tarcopy.Extract(p, in, tarcopy.ExtractOpts{Preserve: q.Preserve, Limits: tarcopy.Limits{MaxBytes: q.MaxBytes}})
	return st, mapTar(err)
}

func mapTar(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, tarcopy.ErrRefused):
		return api.Errorf(api.CodeForbidden, "%v", strings.TrimPrefix(err.Error(), "refused: "))
	case errors.Is(err, tarcopy.ErrTooLarge):
		return api.Errorf(api.CodeInvalid, "%v", strings.TrimPrefix(err.Error(), "too large: "))
	case errors.Is(err, fs.ErrNotExist):
		return api.Errorf(api.CodeNotFound, "%v", err).WithDetail("missing", "file")
	}
	return err
}
