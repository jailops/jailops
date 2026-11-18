package lifecycle

import (
	"context"
	"fmt"
	"path"
	"strings"

	"jailops.net/jailops/internal/sys/zfs"
	"jailops.net/jailops/sdk/api"
)

// BaselineSnap is the snapshot of a persistent dataset taken right after the image's directory was copied into it:
// what the jail's own changes are measured against. It lies on the persistent datasets only, never on the container,
// so the list of the snapshots of a jail (which reads the container) does not show it and no rollback, retention
// policy or copy of the snapshot set touches it.
const BaselineSnap = "jailops-base"

// Where a diff of one dataset was measured from.
const (
	DiffBaseImage    = "image"      // the snapshot of the image's generation that the root is a clone of
	DiffBaseBaseline = "baseline"   // the dataset's own snapshot taken when the jail was made
	DiffBaseTree     = "image-tree" // the same directory of the image, compared file by file: the jail has no baseline
)

// DiffEntry and DiffSet are what the helper answers (zfs diff, or a comparison of two trees).
type DiffEntry = zfs.DiffEntry

type DiffSet = zfs.DiffResult

// unlinkedOpen is the name zfs diff gives to a file that was removed while a process still has it open.
const unlinkedOpen = "/(on_delete_queue)"

// zfsDiffer is the part of the ZFS layer that lists changes; the adapter of the helper has it.
type zfsDiffer interface {
	Diff(ctx context.Context, from, to string, limit int, skip []string) (DiffSet, error)
}

// treeDiffer compares the files of a directory with the files of another one.
type treeDiffer interface {
	TreeDiff(ctx context.Context, dir, base string, limit int) (DiffSet, error)
}

// DatasetDiff is what one dataset of a jail holds that the image does not. Entries are paths inside the jail.
type DatasetDiff struct {
	Name string // root, or the name of a persistent dataset: etc, local, var-log, home, ...
	Path string // where it is mounted in the jail
	Base string // one of the DiffBase constants
	Note string
	DiffSet
}

// JailDiff is the answer of DiffJail: the datasets that can differ from the image, root first.
type JailDiff struct {
	Jail     string
	Image    string // the generation the root was made from, as image#gN
	Datasets []DatasetDiff
}

// DiffJail lists what a thin jail has changed on top of its image: the files its root clone added, changed or removed
// since the generation (zfs diff against the generation's @ro snapshot) and, for each persistent dataset, since the
// jail was made (zfs diff against its baseline snapshot), or against the same directory of the image when the jail
// is older than the baseline. At most limit entries per dataset are returned; the counts are exact.
func (s *Service) DiffJail(ctx context.Context, name string, limit int) (JailDiff, error) {
	st, err := s.observe(ctx, name)
	if err != nil {
		return JailDiff{}, err
	}
	if st.Layout != api.LayoutThin {
		return JailDiff{}, api.Errorf(api.CodeInvalid, "jail %q has a full copy of its image, not a clone: there is nothing to compare its root with", name).
			WithHint("jail diff needs a thin jail (the layout of a FreeBSD jail made with the default storage); a thick or Linux jail has no snapshot of the image to measure against")
	}
	if err := s.refuseLocked(ctx, st); err != nil {
		return JailDiff{}, err
	}
	zd, ok := s.ZFS.(zfsDiffer)
	if !ok {
		return JailDiff{}, api.Errorf(api.CodeUnsupported, "this server cannot list the changes of a jail")
	}
	gen, err := s.Images.Resolve(ctx, s.Cfg.at(name).st.Root, fmt.Sprintf("%s#g%d", st.Image, st.Gen))
	if err != nil {
		return JailDiff{}, err
	}
	out := JailDiff{Jail: name, Image: gen.Name}
	// zfs diff reads the datasets through their mounts, a stopped jail's too
	if up, err := s.Jails.Running(ctx, name); err != nil {
		return JailDiff{}, err
	} else if !up {
		if err := s.mountTree(ctx, name); err != nil {
			return JailDiff{}, err
		}
		defer func() { _ = s.unmountTree(ctx, name) }()
	}
	if err := s.ZFS.Mount(ctx, gen.Dataset); err != nil {
		return JailDiff{}, err
	}
	rootMP := s.prop(ctx, st.Root, "mountpoint")
	// the directories JailOps made in the root to mount the persistent datasets on are not the jail's changes
	skip := []string{unlinkedOpen}
	for _, p := range st.Persist {
		skip = append(skip, path.Join(rootMP, p.Path))
	}
	rootDiff, err := zd.Diff(ctx, gen.Dataset+"@"+roSnap, st.Root, limit, skip)
	if err != nil {
		return JailDiff{}, fmt.Errorf("the changes of the root of jail %s: %w", name, err)
	}
	out.Datasets = append(out.Datasets, DatasetDiff{Name: "root", Path: "/", Base: DiffBaseImage, DiffSet: inJail(rootDiff, rootMP, "/")})
	for _, p := range st.Persist {
		d := DatasetDiff{Name: path.Base(p.DS), Path: path.Clean(p.Path)}
		if has, _ := s.ZFS.Exists(ctx, p.DS+"@"+BaselineSnap); has {
			d.Base = DiffBaseBaseline
			r, err := zd.Diff(ctx, p.DS+"@"+BaselineSnap, p.DS, limit, []string{unlinkedOpen})
			if err != nil {
				return JailDiff{}, fmt.Errorf("the changes of %s of jail %s: %w", p.Path, name, err)
			}
			d.DiffSet = inJail(r, p.MP, d.Path)
			out.Datasets = append(out.Datasets, d)
			continue
		}
		d.Base = DiffBaseTree
		td, ok := s.Files.(treeDiffer)
		genMP := s.prop(ctx, gen.Dataset, "mountpoint")
		if !ok || genMP == "" {
			d.Note = "no baseline: the jail was made before the baseline snapshot existed and the files cannot be compared with the image here"
			d.DiffSet = DiffSet{Entries: []DiffEntry{}}
			out.Datasets = append(out.Datasets, d)
			continue
		}
		r, err := td.TreeDiff(ctx, p.MP, path.Join(genMP, ".zfs", "snapshot", roSnap, d.Path), limit)
		if err != nil {
			return JailDiff{}, fmt.Errorf("the changes of %s of jail %s: %w", p.Path, name, err)
		}
		d.Note = "compared with the image, not with a snapshot of the jail: the jail was made before the baseline snapshot existed"
		d.DiffSet = inJail(r, p.MP, d.Path)
		out.Datasets = append(out.Datasets, d)
	}
	return out, nil
}

// inJail turns the paths zfs prints (below the mount point of the dataset on the host) into paths inside the jail.
func inJail(r DiffSet, mountpoint, jailPath string) DiffSet {
	conv := func(p string) string {
		rel := strings.TrimPrefix(p, strings.TrimSuffix(mountpoint, "/"))
		return path.Join(jailPath, rel)
	}
	for i := range r.Entries {
		r.Entries[i].Path = conv(r.Entries[i].Path)
		if r.Entries[i].To != "" {
			r.Entries[i].To = conv(r.Entries[i].To)
		}
	}
	return r
}
