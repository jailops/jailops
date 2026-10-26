package priv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// RootStatus describes one storage root for check-config and diagnostics.
type RootStatus struct {
	Dataset    string
	Mountpoint string
	Static     bool
	Active     bool
	Error      string
}

// verifyRoot checks that ds can serve as a storage root: it exists as a
// filesystem on an imported pool, carries jailops:managed=on set locally and
// has an absolute mountpoint. It returns the mountpoint.
func (l *Local) verifyRoot(ctx context.Context, ds string) (string, error) {
	if err := l.cfg.checkRootName(ds); err != nil {
		return "", err
	}
	if l.d.ZFS == nil {
		return "", api.Errorf(api.CodeUnsupported, "no zfs implementation")
	}
	pool, _, _ := strings.Cut(ds, "/")
	pools, err := l.d.ZFS.ListPools(ctx)
	if err != nil {
		return "", err
	}
	if !slices.ContainsFunc(pools, func(p zfs.Pool) bool { return p.Name == pool }) {
		return "", api.Errorf(api.CodeNotFound, "pool %q is not imported", pool)
	}
	props, err := l.d.ZFS.Get(ctx, ds, "type", "jailops:managed", "mountpoint")
	if err != nil {
		return "", err
	}
	if props["type"].Value != "filesystem" {
		return "", invalid("%s is not a filesystem", ds)
	}
	if m := props["jailops:managed"]; m.Value != "on" || m.Source != "local" {
		return "", forbidden("dataset %s does not carry jailops:managed=on locally", ds)
	}
	mp := props["mountpoint"].Value
	if !cleanAbs(mp) || mp == "/" {
		return "", invalid("dataset %s needs an absolute mountpoint other than / (is %q)", ds, mp)
	}
	return mp, nil
}

// checkRootSystem refuses a root that would shadow the running system: the
// dataset or any dataset below it mounted on or above a system path, or its
// mountpoint inside the conf directory.
func (l *Local) checkRootSystem(ctx context.Context, ds, mount string) error {
	if l.cfg.overlapsSystem(mount) {
		return forbidden("mountpoint %s of %s covers or lies inside a system path", mount, ds)
	}
	list, err := l.d.ZFS.List(ctx, zfs.ListOpts{Root: ds, Recursive: true, Props: []string{"mountpoint"}})
	if err != nil {
		return err
	}
	for _, d := range list {
		if m := d.Props["mountpoint"]; cleanAbs(m) && l.cfg.overlapsSystem(m) {
			return forbidden("%s (mountpoint %s) is part of the running system; %s cannot be a storage root", d.Name, m, ds)
		}
	}
	return nil
}

// loadStorageFile reads the registered roots. A missing file is an empty list.
// The file must be owned by the helper's user and not accessible to others.
func (l *Local) loadStorageFile() ([]string, error) {
	f, err := os.Open(l.cfg.StorageFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0o077 != 0 {
		return nil, forbidden("storage file %s must be a regular file with mode 0600", l.cfg.StorageFile)
	}
	if sys, ok := st.Sys().(*syscall.Stat_t); ok && int(sys.Uid) != os.Geteuid() {
		return nil, forbidden("storage file %s is not owned by the helper's user", l.cfg.StorageFile)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStorageFileBytes+1))
	if err != nil {
		return nil, api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	return l.cfg.parseStorageFile(b)
}

// writeStorageFile replaces the storage file atomically: temp file in the
// same directory, mode 0600, fsync, rename, directory fsync.
func (l *Local) writeStorageFile(roots []string) error {
	path := l.cfg.StorageFile
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return forbidden("%s exists and is not a regular file", path)
	}
	tmp := path + ".jotmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	fail := func(err error) error {
		f.Close()
		_ = os.Remove(tmp)
		return api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	if _, err := f.Write(renderStorageFile(roots)); err != nil {
		return fail(err)
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return api.Errorf(api.CodeInternal, "storage file: %v", err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// ReloadRoots rereads the storage file and verifies every root, static and
// registered. A root that fails verification is kept as inactive and allows
// nothing. A corrupt storage file is an error and leaves the current set in
// place. It is called at start and on SIGHUP.
func (l *Local) ReloadRoots(ctx context.Context) error {
	l.rootsMu.Lock()
	defer l.rootsMu.Unlock()
	return l.reloadLocked(ctx)
}

func (l *Local) reloadLocked(ctx context.Context) error {
	file, err := l.loadStorageFile()
	if err != nil {
		return err
	}
	var cand []rootEntry
	for _, r := range l.cfg.StorageRoots {
		cand = append(cand, rootEntry{Dataset: r, Static: true})
	}
	for _, r := range file {
		if !slices.ContainsFunc(cand, func(e rootEntry) bool { return e.Dataset == r }) {
			cand = append(cand, rootEntry{Dataset: r})
		}
	}
	if err := checkRootOverlap(rootNames(cand)); err != nil {
		return err
	}
	for i := range cand {
		mp, err := l.verifyRoot(ctx, cand[i].Dataset)
		if err == nil {
			err = l.checkRootSystem(ctx, cand[i].Dataset, mp)
		}
		if err == nil {
			for j := range i {
				if cand[j].active() && (within(cand[j].Mount, mp) || within(mp, cand[j].Mount)) {
					err = invalid("mountpoint %s overlaps %s", mp, cand[j].Mount)
				}
			}
		}
		if err != nil {
			cand[i].Err = err.Error()
			continue
		}
		cand[i].Mount = mp
	}
	l.cfg.reg.set(cand)
	return nil
}

func rootNames(es []rootEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.Dataset
	}
	return out
}

// RootStatuses reports every configured root, active or not.
func (l *Local) RootStatuses() []RootStatus {
	var out []RootStatus
	for _, e := range l.cfg.reg.all() {
		out = append(out, RootStatus{Dataset: e.Dataset, Mountpoint: e.Mount, Static: e.Static, Active: e.active(), Error: e.Err})
	}
	return out
}

// Storages returns the datasets of the active storage roots, sorted.
func (l *Local) Storages(ctx context.Context) ([]string, error) {
	out := l.cfg.Roots()
	if out == nil {
		out = []string{}
	}
	sort.Strings(out)
	return out, nil
}

// RegisterStorage adds a storage root and persists it. It needs the admin
// assertion. The dataset must exist on an imported pool, carry
// jailops:managed=on locally, have an absolute mountpoint, not be a pool root
// (unless allow-pool-root), not lie inside or contain another root (by name
// or by mountpoint), and not be or contain a dataset of the running system.
func (l *Local) RegisterStorage(ctx context.Context, dataset string) error {
	if !isAdmin(ctx) {
		return forbidden("registering a storage root needs the admin assertion")
	}
	if err := l.cfg.checkRootName(dataset); err != nil {
		return err
	}
	l.rootsMu.Lock()
	defer l.rootsMu.Unlock()
	cur := l.cfg.reg.all()
	if len(cur) >= MaxStorageRoots {
		return invalid("too many storage roots (limit %d)", MaxStorageRoots)
	}
	for _, e := range cur {
		switch {
		case e.Dataset == dataset:
			return api.Errorf(api.CodeAlreadyExists, "storage root %s is already registered", dataset)
		case dsInside(dataset, e.Dataset):
			return forbidden("%s lies inside the storage root %s", dataset, e.Dataset)
		case dsInside(e.Dataset, dataset):
			return forbidden("%s contains the storage root %s", dataset, e.Dataset)
		}
	}
	mp, err := l.verifyRoot(ctx, dataset)
	if err != nil {
		return err
	}
	if err := l.checkRootSystem(ctx, dataset, mp); err != nil {
		return err
	}
	for _, e := range cur {
		if e.active() && (within(e.Mount, mp) || within(mp, e.Mount)) {
			return forbidden("mountpoint %s overlaps the mountpoint %s of %s", mp, e.Mount, e.Dataset)
		}
	}
	var persist []string
	for _, e := range cur {
		if !e.Static {
			persist = append(persist, e.Dataset)
		}
	}
	persist = append(persist, dataset)
	if err := l.writeStorageFile(persist); err != nil {
		return err
	}
	l.cfg.reg.set(append(cur, rootEntry{Dataset: dataset, Mount: mp}))
	return nil
}

// UnregisterStorage removes a storage root registered at run time and
// persists the change. Roots fixed in priv.conf cannot be removed. Nothing
// below the root is touched.
func (l *Local) UnregisterStorage(ctx context.Context, dataset string) error {
	if !isAdmin(ctx) {
		return forbidden("unregistering a storage root needs the admin assertion")
	}
	if err := zfs.ValidateDataset(dataset); err != nil {
		return err
	}
	l.rootsMu.Lock()
	defer l.rootsMu.Unlock()
	cur := l.cfg.reg.all()
	var keep []rootEntry
	var persist []string
	found := false
	for _, e := range cur {
		if e.Dataset == dataset {
			if e.Static {
				return forbidden("storage root %s is fixed in the helper configuration", dataset)
			}
			found = true
			continue
		}
		keep = append(keep, e)
		if !e.Static {
			persist = append(persist, e.Dataset)
		}
	}
	if !found {
		return api.Errorf(api.CodeNotFound, "storage root %s is not registered", dataset)
	}
	if err := l.writeStorageFile(persist); err != nil {
		return err
	}
	l.cfg.reg.set(keep)
	return nil
}

func (r RootStatus) String() string {
	s := "inactive"
	if r.Active {
		s = "active"
	}
	src := "registered"
	if r.Static {
		src = "priv.conf"
	}
	out := fmt.Sprintf("%s mount=%s %s (%s)", r.Dataset, r.Mountpoint, s, src)
	if r.Error != "" {
		out += ": " + r.Error
	}
	return out
}
