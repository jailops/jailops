package priv

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"jailops.net/jailops/sdk/api"
)

// ReadFileReq reads a file under an allow-listed root.
type ReadFileReq struct {
	Path string `json:"path"`
}

// WriteFileReq writes a file under an allow-listed root.
type WriteFileReq struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
	// Mode is the permission bits. setuid, setgid, sticky and world-write
	// bits are refused.
	Mode uint32 `json:"mode"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
	// Parents creates missing parent directories (mode 0755, root).
	Parents bool `json:"parents,omitempty"`
}

// MkdirReq creates a directory (and parents) under an allow-listed root.
type MkdirReq struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`
	UID  uint32 `json:"uid"`
	GID  uint32 `json:"gid"`
}

// RemoveReq removes one file or one empty directory.
type RemoveReq struct {
	Path string `json:"path"`
}

const maxPath = 4096

// resolved is a path split into the allow-listed root that holds it and the
// slash-free relative name inside that root.
type resolved struct {
	root string // real path of the root (symlinks evaluated at startup)
	rel  string // clean, relative, no ".."; "." is the root itself
}

func (c Config) resolve(path string) (resolved, error) {
	if path == "" || len(path) > maxPath || strings.ContainsRune(path, 0) || !filepath.IsAbs(path) {
		return resolved{}, invalid("invalid path %q", path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." {
			return resolved{}, forbidden("path %q contains ..", path)
		}
	}
	p := filepath.Clean(path)
	for _, r := range c.roots() {
		if within(r, p) {
			if err := c.checkOwnRoot(r); err != nil {
				return resolved{}, err
			}
			rel, _ := filepath.Rel(r, p)
			return resolved{root: r, rel: rel}, nil
		}
	}
	return resolved{}, forbidden("path %q is outside the allowed roots", path)
}

// openRoot opens the root directory without following a symlink at its last
// component beyond what the operator configured: the root itself may be a
// symlink the operator set up, everything below it may not escape.
func (r resolved) openRoot() (*os.Root, error) {
	return os.OpenRoot(r.root)
}

func mapFSErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return api.Errorf(api.CodeNotFound, "%v", err)
	case errors.Is(err, fs.ErrExist):
		return api.Errorf(api.CodeAlreadyExists, "%v", err)
	case errors.Is(err, fs.ErrPermission):
		return api.Errorf(api.CodeForbidden, "%v", err)
	}
	// os.Root reports an escaping path with a plain error.
	if strings.Contains(err.Error(), "escapes from parent") || strings.Contains(err.Error(), "path escapes") {
		return forbidden("path escapes the allowed root")
	}
	return api.Errorf(api.CodeInternal, "%v", err)
}

func checkMode(mode uint32) error {
	if mode&^0o777 != 0 {
		return forbidden("mode %#o has special bits", mode)
	}
	if mode&0o002 != 0 {
		return forbidden("mode %#o is world-writable", mode)
	}
	return nil
}

func checkOwner(uid, gid uint32) error {
	if uid >= 0xfffffffe || gid >= 0xfffffffe {
		return invalid("invalid owner %d:%d", uid, gid)
	}
	return nil
}

// ReadFile reads a file below an allow-listed root, never following a
// symlink out of it, and never more than Limits.MaxFileBytes.
func (l *Local) ReadFile(ctx context.Context, path string) ([]byte, error) {
	r, err := l.cfg.resolveRead(path)
	if err != nil {
		return nil, err
	}
	if r.rel == "." {
		return nil, invalid("%q is a root", path)
	}
	root, err := r.openRoot()
	if err != nil {
		return nil, mapFSErr(err)
	}
	defer root.Close()
	f, err := root.Open(r.rel)
	if err != nil {
		return nil, mapFSErr(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, mapFSErr(err)
	}
	if !st.Mode().IsRegular() {
		return nil, invalid("%q is not a regular file", path)
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(l.cfg.Limits.MaxFileBytes)+1))
	if err != nil {
		return nil, mapFSErr(err)
	}
	if len(b) > l.cfg.Limits.MaxFileBytes {
		return nil, invalid("file larger than %d bytes", l.cfg.Limits.MaxFileBytes)
	}
	return b, nil
}

// WriteFile writes a file atomically (temp file, fsync, rename) below an
// allow-listed root. Files in ConfDir are content-checked.
func (l *Local) WriteFile(ctx context.Context, q WriteFileReq) error {
	if err := l.checkFrozen(q.Path); err != nil {
		return err
	}
	r, err := l.cfg.resolve(q.Path)
	if err != nil {
		return err
	}
	if r.rel == "." {
		return invalid("%q is a root", q.Path)
	}
	if len(q.Data) > l.cfg.Limits.MaxFileBytes {
		return invalid("file larger than %d bytes", l.cfg.Limits.MaxFileBytes)
	}
	if err := checkMode(q.Mode); err != nil {
		return err
	}
	if err := checkOwner(q.UID, q.GID); err != nil {
		return err
	}
	if err := l.checkConfContent(ctx, r, q.Path, q.Data); err != nil {
		return err
	}
	root, err := r.openRoot()
	if err != nil {
		return mapFSErr(err)
	}
	defer root.Close()
	dir, base := filepath.Split(r.rel)
	dir = filepath.Clean(dir)
	if q.Parents && dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return mapFSErr(err)
		}
	}
	// Refuse to replace anything that is not a regular file.
	if st, err := root.Lstat(r.rel); err == nil && !st.Mode().IsRegular() {
		return forbidden("%q exists and is not a regular file", q.Path)
	}
	tmp := filepath.Join(dir, "."+base+".jotmp")
	_ = root.Remove(tmp)
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, os.FileMode(q.Mode))
	if err != nil {
		return mapFSErr(err)
	}
	cleanup := func() { _ = root.Remove(tmp) }
	if _, err := f.Write(q.Data); err != nil {
		f.Close()
		cleanup()
		return mapFSErr(err)
	}
	if err := f.Chmod(os.FileMode(q.Mode)); err != nil {
		f.Close()
		cleanup()
		return mapFSErr(err)
	}
	if err := f.Chown(int(q.UID), int(q.GID)); err != nil && !l.noChown {
		f.Close()
		cleanup()
		return mapFSErr(err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		cleanup()
		return mapFSErr(err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return mapFSErr(err)
	}
	if err := root.Rename(tmp, r.rel); err != nil {
		cleanup()
		return mapFSErr(err)
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// MkdirAll creates a directory below an allow-listed root.
func (l *Local) MkdirAll(ctx context.Context, q MkdirReq) error {
	if err := l.checkFrozen(q.Path); err != nil {
		return err
	}
	r, err := l.cfg.resolve(q.Path)
	if err != nil {
		return err
	}
	if err := checkMode(q.Mode); err != nil {
		return err
	}
	if err := checkOwner(q.UID, q.GID); err != nil {
		return err
	}
	root, err := r.openRoot()
	if err != nil {
		return mapFSErr(err)
	}
	defer root.Close()
	if r.rel == "." {
		return nil
	}
	// A directory that already exists keeps its mode and owner: re-chowning one the daemon could have
	// swapped or pre-created would hand it to whoever the request names.
	if fi, err := root.Lstat(r.rel); err == nil {
		if !fi.IsDir() {
			return invalid("%q exists and is not a directory", q.Path)
		}
		return nil
	}
	if err := root.MkdirAll(r.rel, os.FileMode(q.Mode)); err != nil {
		return mapFSErr(err)
	}
	if err := root.Chmod(r.rel, os.FileMode(q.Mode)); err != nil {
		return mapFSErr(err)
	}
	if err := root.Chown(r.rel, int(q.UID), int(q.GID)); err != nil && !l.noChown {
		return mapFSErr(err)
	}
	return nil
}

// Remove removes a file or an empty directory below an allow-listed root.
// Roots themselves are never removed. A missing path is not an error.
func (l *Local) Remove(ctx context.Context, path string) error {
	if err := l.checkFrozen(path); err != nil {
		return err
	}
	r, err := l.cfg.resolve(path)
	if err != nil {
		return err
	}
	if r.rel == "." {
		return forbidden("%q is a root", path)
	}
	root, err := r.openRoot()
	if err != nil {
		return mapFSErr(err)
	}
	defer root.Close()
	if err := root.Remove(r.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return mapFSErr(err)
	}
	return nil
}

// recursiveRemovable reports whether a whole tree may be removed at path: the scratch trees the daemon makes
// below its work directory (image builds), and the stage and merge directories of a jail's container.
func (c Config) recursiveRemovable(path string) bool {
	p := filepath.Clean(path)
	if c.WorkDir != "" && within(c.WorkDir, p) && p != c.WorkDir {
		return true
	}
	base := filepath.Base(p)
	return c.inMounts(p) && (strings.HasPrefix(base, ".seed-") || strings.HasPrefix(base, ".merge-"))
}

// RemoveAll removes path and everything below it where recursiveRemovable allows that; anywhere else it is
// Remove (one file or one empty directory).
func (l *Local) RemoveAll(ctx context.Context, path string) error {
	if err := l.checkFrozen(path); err != nil {
		return err
	}
	if !l.cfg.recursiveRemovable(path) {
		return l.Remove(ctx, path)
	}
	r, err := l.cfg.resolve(path)
	if err != nil {
		return err
	}
	if r.rel == "." {
		return forbidden("%q is a root", path)
	}
	root, err := r.openRoot()
	if err != nil {
		return mapFSErr(err)
	}
	defer root.Close()
	if err := root.RemoveAll(r.rel); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return mapFSErr(err)
	}
	return nil
}

// confFileFor returns the path of the jail.conf of a jail inside ConfDir if it
// exists as a regular file (found without following symlinks).
func (l *Local) confFileFor(name string) (string, bool) {
	root, err := os.OpenRoot(l.cfg.ConfDir)
	if err != nil {
		return "", false
	}
	defer root.Close()
	for _, rel := range []string{name + ".conf", filepath.Join(name, "jail.conf")} {
		if st, err := root.Lstat(rel); err == nil && st.Mode().IsRegular() {
			return filepath.Join(l.cfg.ConfDir, rel), true
		}
	}
	return "", false
}

// ReadOnlyFiles are host files fs.read_file may read besides the allowed
// roots: the configuration an image build copies or checks, and /etc/rc.conf, which doctor's persistence check reads
// for `gateway_enable` and `ipv6_gateway_enable` (without it the check could never see them and always warned, even
// on a host where they were set: found live on darius, October 2026).
var ReadOnlyFiles = []string{"/etc/sysctl.conf", "/etc/resolv.conf", "/etc/rc.conf"}

// resolveRead is resolve for reading: the allowed roots, the fixed files of
// ReadOnlyFiles and the configured image mirrors.
func (c Config) resolveRead(path string) (resolved, error) {
	r, err := c.resolve(path)
	if err == nil || !api.IsCode(err, api.CodeForbidden) {
		return r, err
	}
	p := filepath.Clean(path)
	if slices.Contains(ReadOnlyFiles, p) {
		return resolved{root: filepath.Dir(p), rel: filepath.Base(p)}, nil
	}
	for _, m := range c.ImageMirrors {
		if within(m, p) {
			rel, _ := filepath.Rel(m, p)
			return resolved{root: m, rel: rel}, nil
		}
	}
	return resolved{}, err
}

// checkOwnRoot refuses to work in one of the helper's own directories (the jail.conf directory and the daemon's
// work directory) unless it is a real directory of root that nobody else can write. The daemon may create or replace
// what it owns, so a directory it can write could be turned into a symlink to / between a check and its use, and
// everything below it would then be done to the host as root.
func (c Config) checkOwnRoot(r string) error {
	if c.noOwnerCheck || (r != c.ConfDir && r != c.WorkDir) {
		return nil
	}
	fi, err := os.Lstat(r)
	if err != nil {
		return nil // a missing directory is an ordinary error of the operation
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return forbidden("%s is not a plain directory", r)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || fi.Mode().Perm()&0o022 != 0 {
		return forbidden("%s must belong to root and not be writable by anybody else", r)
	}
	return nil
}
