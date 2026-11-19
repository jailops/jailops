package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// The base package records of a thin jail.
//
// The root of a thin jail is a clone of an image generation, and /var/db is a persistent dataset of the jail mounted
// over the root's own /var/db. After a root swap (a generation upgrade, jail upgrade --image, a root rollback) the
// files of the base come from the new generation while the jail's package database still holds the base records of
// the old one: pkg which finds nothing for a new file, pkg info shows the old patch level and pkg check -s only
// checks what the stale records list.
//
// The generation's own database is the truth for the base set. At every start of a thin jail the records of that set
// are compared by name and version, and the ones that differ are replaced: the record is unregistered (no file is
// touched, no script runs) and registered again from the manifest of the generation (register -t: no file analysis,
// no script). Records of other packages are never touched. A crash between the two steps leaves a base package
// without a record, never with a wrong one, and the next start adds it.

// BaseSync is what a pass over the base records did.
type BaseSync struct {
	// Base is the number of packages of the generation, Replaced those whose record had another version and Added
	// those that had none.
	Base, Replaced, Added int
}

// recordBatch is how many records one unregister names.
const recordBatch = 60

// pkgNV splits "name-version" at the last dash.
func pkgNV(l string) (string, string, bool) {
	l = strings.TrimSpace(l)
	i := strings.LastIndex(l, "-")
	if i <= 0 || i == len(l)-1 {
		return "", "", false
	}
	return l[:i], l[i+1:], true
}

func parseRecords(b []byte) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if n, v, ok := pkgNV(l); ok {
			m[n] = v
		}
	}
	return m
}

// recordFileName is a name that is safe as a file name of a manifest.
func recordFileName(n string) bool {
	if n == "" || strings.HasPrefix(n, "-") || strings.Contains(n, "..") {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '+') {
			return false
		}
	}
	return true
}

// syncBaseRecords brings the base package records of a thin jail in step with the generation its root was cloned from.
// The tree of the jail is mounted. A jail that is not thin has nothing to do.
func (s *Service) syncBaseRecords(ctx context.Context, name string) (BaseSync, error) {
	var res BaseSync
	st, err := s.observe(ctx, name)
	if err != nil {
		return res, err
	}
	if st.Layout != api.LayoutThin || st.Image == "" {
		return res, nil
	}
	gen, err := s.Images.Resolve(ctx, s.Cfg.at(name).st.Root, genName(st.Image, st.Gen))
	if err != nil {
		return res, fmt.Errorf("the generation %s: %w", genName(st.Image, st.Gen), err)
	}
	genMP := s.prop(ctx, gen.Dataset, "mountpoint")
	if genMP == "" || genMP == "none" || genMP == "legacy" || genMP == "-" {
		return res, fmt.Errorf("generation %s has no mountpoint", gen.Name)
	}
	if err := s.ZFS.Mount(ctx, gen.Dataset); err != nil {
		if ok, _ := s.Files.Exists(ctx, path.Join(genMP, "var/db/pkg")); !ok {
			return res, err
		}
	}
	rootMP := s.Cfg.at(name).rootMP()
	genOut, err := s.Exec.Run(ctx, []string{"pkg-records", "-r", genMP, "info", "-a", "-q"})
	if err != nil {
		return res, fmt.Errorf("the packages of %s: %w", gen.Name, err)
	}
	jailOut, err := s.Exec.Run(ctx, []string{"pkg-records", "-r", rootMP, "info", "-a", "-q"})
	if err != nil {
		return res, fmt.Errorf("the packages of the jail: %w", err)
	}
	base, have := parseRecords(genOut), parseRecords(jailOut)
	res.Base = len(base)
	var replace, add []string
	for n, v := range base {
		switch hv, ok := have[n]; {
		case !ok:
			add = append(add, n)
		case hv != v:
			replace = append(replace, n)
		}
	}
	if len(replace)+len(add) == 0 {
		return res, nil
	}
	sort.Strings(replace)
	sort.Strings(add)
	todo := append(append([]string(nil), replace...), add...)
	work := s.Cfg.at(name).containerMP() + "/.base-sync-" + fmt.Sprint(s.Cfg.now().UnixNano())
	if err := s.Files.MkdirAll(ctx, work); err != nil {
		return res, err
	}
	defer func() { _ = s.Files.RemoveAll(ctx, work) }()
	// The manifests first: nothing is changed in the jail's database when one cannot be read.
	files := map[string]string{}
	for _, n := range todo {
		if !recordFileName(n) {
			return res, fmt.Errorf("package name %q is not usable", n)
		}
		out, err := s.Exec.Run(ctx, []string{"pkg-records", "-r", genMP, "info", "--raw", "--raw-format", "ucl", n})
		if err != nil {
			return res, fmt.Errorf("the manifest of %s: %w", n, err)
		}
		f := path.Join(work, n+".ucl")
		if err := s.Files.WriteAtomic(ctx, f, out); err != nil {
			return res, err
		}
		files[n] = f
	}
	var errs []error
	for i := 0; i < len(replace); i += recordBatch {
		j := min(i+recordBatch, len(replace))
		argv := append([]string{"pkg-records", "-r", rootMP, "unregister", "-f", "-y"}, replace[i:j]...)
		if _, err := s.Exec.Run(ctx, argv); err != nil {
			return res, fmt.Errorf("remove the old records: %w", err)
		}
	}
	for _, n := range todo {
		if _, err := s.Exec.Run(ctx, []string{"pkg-records", "-r", rootMP, "register", "-t", "-M", files[n]}); err != nil {
			errs = append(errs, fmt.Errorf("record %s: %w", n, err))
			continue
		}
		if _, was := have[n]; was {
			res.Replaced++
		} else {
			res.Added++
		}
	}
	return res, errors.Join(errs...)
}

// baseRecordsAtStart is the pass a start makes, for every way a jail starts (a plain start, an upgrade, a rollback, a
// conversion). A failure is told and never stops the start.
func (s *Service) baseRecordsAtStart(ctx context.Context, name string) {
	_, err := s.syncBaseRecords(ctx, name)
	if s.BaseRecords != nil {
		s.BaseRecords(ctx, name, err)
	}
	if err != nil && s.Warn != nil {
		s.Warn(ctx, name, "base-packages", err.Error())
	}
}

// RepairBasePackages is the manual pass over the base records of a thin jail that is stopped (the helper opens
// the database as root only in a root no jail runs in): what a start does, with the result returned.
func (s *Service) RepairBasePackages(ctx context.Context, name string) (BaseSync, error) {
	if _, err := s.ready(ctx, name); err != nil {
		return BaseSync{}, err
	}
	if err := s.mountTree(ctx, name); err != nil {
		return BaseSync{}, err
	}
	return s.syncBaseRecords(ctx, name)
}
