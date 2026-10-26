package imagegen

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// JailRef is what the image manager needs to know about a jail to count
// references. References come from jail specs, never from ZFS clone origins.
type JailRef struct {
	Name string
	// Image is the spec reference: "<image>@<alias>", "<image>#g<N>" or "<image>".
	Image string
	// Current and Previous are generation names ("<image>#g<N>") of the root
	// in use and of the root a rollback would return to. Either may be empty.
	Current  string
	Previous string
}

// Refs maps generation number to the sorted jail names that reference it.
func Refs(image string, st api.ImageStatus, jails []JailRef) map[int][]string {
	set := map[int]map[string]bool{}
	add := func(n int, jail string) {
		if set[n] == nil {
			set[n] = map[string]bool{}
		}
		set[n][jail] = true
	}
	for _, j := range jails {
		if r, err := ParseRef(j.Image); err == nil && r.Image == image {
			if n, err := resolveNumber(r, st); err == nil {
				add(n, j.Name)
			}
		}
		for _, g := range []string{j.Current, j.Previous} {
			if g == "" {
				continue
			}
			if r, err := ParseRef(g); err == nil && r.Image == image && r.Gen > 0 {
				add(r.Gen, j.Name)
			}
		}
	}
	out := map[int][]string{}
	for n, s := range set {
		for j := range s {
			out[n] = append(out[n], j)
		}
		sort.Strings(out[n])
	}
	return out
}

// ApplyRefs fills ImageGeneration.Refs in a status.
func ApplyRefs(image string, st *api.ImageStatus, jails []JailRef) {
	refs := Refs(image, *st, jails)
	for i := range st.Generations {
		st.Generations[i].Refs = refs[st.Generations[i].Number]
	}
}

// DeleteResult describes what Delete did.
type DeleteResult struct {
	// Parked is true when clones exist and the generation was renamed into deleted/.
	Parked bool
	// Collected lists datasets garbage-collected afterwards.
	Collected []string
	// CollectErr is a non-fatal failure of the follow-up collection.
	CollectErr error
	Plan       []string
}

// Delete implements design 4.3 step 9: a generation is destroyed only when no
// jail references it. While clones exist it is renamed into deleted/ and its
// origin is garbage-collected when the last clone is destroyed (Collect).
func (m *Manager) Delete(ctx context.Context, image string, n int, jails []JailRef, dryRun bool) (DeleteResult, error) {
	var res DeleteResult
	o, err := m.Observe(ctx, image)
	if err != nil {
		return res, err
	}
	if _, ok := o.gen(n); !ok {
		return res, api.Errorf(api.CodeNotFound, "generation %s does not exist", GenerationName(image, n))
	}
	if refs := Refs(image, o.Status, jails)[n]; len(refs) > 0 {
		return res, api.Errorf(api.CodeImageInUse, "generation %s is used by %s", GenerationName(image, n), strings.Join(refs, ", ")).
			WithDetail("jails", strings.Join(refs, ",")).WithHint("upgrade or destroy those jails first")
	}
	ds := m.Layout.GenDS(image, n)
	ro := ds + "@" + ROSnap
	clones, err := m.ZFS.Clones(ctx, ro)
	if err != nil {
		return res, err
	}
	z := m.ZFS
	rehold := func(snap string) func(context.Context) error {
		return func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, snap); !ok {
				return nil
			}
			hs, err := z.Holds(ctx, snap)
			if err != nil {
				return err
			}
			for _, h := range hs {
				if h == HoldTag {
					return nil
				}
			}
			return z.Hold(ctx, snap, HoldTag)
		}
	}
	moved, newAliases := aliasesWithout(o.Status, n)
	imageDS := m.Layout.ImageDS(image)
	var oldAliases string
	var hadAliases bool
	moveAliases := plan.Step{Name: "move-aliases", Describe: "move or remove the aliases that point at " + GenerationName(image, n) + ": " + strings.Join(moved, ", "),
		Do: func(ctx context.Context) error {
			old, err := z.Get(ctx, imageDS, PropAliases)
			if err != nil {
				return err
			}
			oldAliases, hadAliases = old, old != ""
			return m.writeAliases(ctx, image, newAliases)
		},
		Undo: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, imageDS); !ok {
				return nil
			}
			if hadAliases {
				return z.Set(ctx, imageDS, map[string]string{PropAliases: oldAliases})
			}
			return z.Inherit(ctx, imageDS, PropAliases)
		}, UndoOnFail: true}
	release := plan.Step{Name: "release-hold", Describe: "zfs release " + HoldTag + " " + ro,
		Do: func(ctx context.Context) error {
			hs, err := z.Holds(ctx, ro)
			if err != nil {
				return err
			}
			for _, h := range hs {
				if h == HoldTag {
					return z.Release(ctx, ro, HoldTag)
				}
			}
			return nil
		},
		Undo: rehold(ro), UndoOnFail: true}
	p := plan.New("image-delete " + GenerationName(image, n))
	if len(moved) > 0 {
		p.Add(moveAliases)
	}
	if len(clones) == 0 {
		p.Add(release,
			plan.Step{Name: "unmount", Describe: "zfs unmount " + ds, Do: func(ctx context.Context) error { return z.Unmount(ctx, ds) }},
			plan.Step{Name: "destroy", Describe: "zfs destroy -r " + ds,
				Do: func(ctx context.Context) error { return z.DestroyRecursive(ctx, ds) }})
	} else {
		parked := m.Layout.DeletedDS(image, n)
		dp := m.Layout.DeletedParent()
		res.Parked = true
		madeParent := false
		p.Add(
			plan.Step{Name: "ensure-deleted-dataset", Describe: "zfs create " + dp + " (if missing)",
				Do: func(ctx context.Context) error {
					if ok, err := z.Exists(ctx, dp); err != nil || ok {
						return err
					}
					madeParent = true
					return z.Create(ctx, dp, map[string]string{PropManaged: "on", PropKind: "deleted", "canmount": "off", "mountpoint": "none"})
				},
				Undo: func(ctx context.Context) error {
					if !madeParent {
						return nil
					}
					if ok, _ := z.Exists(ctx, dp); !ok {
						return nil
					}
					return z.Destroy(ctx, dp)
				}},
			plan.Step{Name: "rename-to-deleted", Describe: fmt.Sprintf("zfs rename %s %s  (%d clones keep the origin alive)", ds, parked, len(clones)),
				Do: func(ctx context.Context) error { return z.Rename(ctx, ds, parked) },
				Undo: func(ctx context.Context) error {
					if ok, _ := z.Exists(ctx, parked); !ok {
						return nil
					}
					return z.Rename(ctx, parked, ds)
				}},
			plan.Step{Name: "mark-deleted", Describe: "zfs set jailops:deleted=on " + parked,
				Do: func(ctx context.Context) error { return z.Set(ctx, parked, map[string]string{PropDeleted: "on"}) },
				Undo: func(ctx context.Context) error {
					if ok, _ := z.Exists(ctx, parked); !ok {
						return nil
					}
					return z.Inherit(ctx, parked, PropDeleted)
				}})
	}
	res.Plan = p.Describe()
	if dryRun {
		return res, nil
	}
	if err := m.executor().Run(ctx, fmt.Sprintf("image-delete/%s/g%d", image, n), p); err != nil {
		return res, err
	}
	res.Collected, res.CollectErr = m.Collect(ctx)
	return res, nil
}

// aliasesWithout returns the aliases that point at generation n (sorted) and the alias map after removing n: each
// such alias moves to the newest remaining generation, or disappears when none remains.
func aliasesWithout(st api.ImageStatus, n int) ([]string, map[string]int) {
	newest := 0
	for _, g := range st.Generations {
		if g.Number != n && g.Number > newest {
			newest = g.Number
		}
	}
	var moved []string
	out := map[string]int{}
	for a, an := range st.Aliases {
		if an != n {
			out[a] = an
			continue
		}
		moved = append(moved, a)
		if newest > 0 {
			out[a] = newest
		}
	}
	sort.Strings(moved)
	return moved, out
}

// writeAliases stores the alias map on the image dataset; an empty map clears the property.
func (m *Manager) writeAliases(ctx context.Context, image string, al map[string]int) error {
	ids := m.Layout.ImageDS(image)
	if len(al) == 0 {
		return m.ZFS.Inherit(ctx, ids, PropAliases)
	}
	return m.ZFS.Set(ctx, ids, map[string]string{PropAliases: formatAliases(al)})
}

// DeleteAlias removes an alias from the image on this pool. It reports whether the alias existed.
func (m *Manager) DeleteAlias(ctx context.Context, image, alias string) (bool, error) {
	o, err := m.Observe(ctx, image)
	if err != nil {
		return false, err
	}
	if _, ok := o.Status.Aliases[alias]; !ok {
		return false, nil
	}
	al := map[string]int{}
	for a, n := range o.Status.Aliases {
		if a != alias {
			al[a] = n
		}
	}
	return true, m.writeAliases(ctx, image, al)
}

// DeleteImage removes the image dataset (aliases and the generation counter) once it holds no generation any more.
// It leaves the dataset alone when anything is still below it.
func (m *Manager) DeleteImage(ctx context.Context, image string) error {
	ids := m.Layout.ImageDS(image)
	ok, err := m.ZFS.Exists(ctx, ids)
	if err != nil || !ok {
		return err
	}
	if v, _ := m.ZFS.Get(ctx, ids, PropManaged); v != "on" {
		return nil
	}
	below, err := m.ZFS.Descendants(ctx, ids)
	if err != nil || len(directChildren(ids, below)) > 0 {
		return err
	}
	return m.ZFS.Destroy(ctx, ids)
}

// Collect garbage-collects generations parked in deleted/ whose last clone is
// gone. It repeats until nothing more can be removed, because destroying a
// parked generation can free the one it was cloned from.
func (m *Manager) Collect(ctx context.Context) ([]string, error) {
	dp := m.Layout.DeletedParent()
	if ok, err := m.ZFS.Exists(ctx, dp); err != nil || !ok {
		return nil, err
	}
	var done []string
	for {
		all, err := m.ZFS.Descendants(ctx, dp)
		if err != nil {
			return done, err
		}
		progress := false
		for _, c := range directChildren(dp, all) {
			ds := dp + "/" + c
			if v, _ := m.ZFS.Get(ctx, ds, PropManaged); v != "on" {
				continue
			}
			if v, _ := m.ZFS.Get(ctx, ds, PropDeleted); v != "on" {
				continue
			}
			ro := ds + "@" + ROSnap
			if ok, _ := m.ZFS.Exists(ctx, ro); ok {
				cl, err := m.ZFS.Clones(ctx, ro)
				if err != nil {
					return done, err
				}
				if len(cl) > 0 {
					continue
				}
				hs, err := m.ZFS.Holds(ctx, ro)
				if err != nil {
					return done, err
				}
				for _, h := range hs {
					if h == HoldTag {
						if err := m.ZFS.Release(ctx, ro, HoldTag); err != nil {
							return done, err
						}
					}
				}
			}
			if err := m.ZFS.DestroyRecursive(ctx, ds); err != nil {
				return done, err
			}
			done = append(done, ds)
			progress = true
		}
		if !progress {
			return done, nil
		}
	}
}

// Candidates returns the generations the retention policy allows deleting:
// never the newest, never referenced by a jail, never an alias target, outside
// the KeepLast newest and older than KeepDays. A policy with both values zero
// keeps everything.
func Candidates(r api.ImageRetention, image string, st api.ImageStatus, jails []JailRef, now time.Time) []int {
	if r.KeepLast <= 0 && r.KeepDays <= 0 {
		return nil
	}
	gens := append([]api.ImageGeneration(nil), st.Generations...)
	sort.Slice(gens, func(i, j int) bool { return gens[i].Number > gens[j].Number })
	refs := Refs(image, st, jails)
	aliased := map[int]bool{}
	for _, n := range st.Aliases {
		aliased[n] = true
	}
	var out []int
	for i, g := range gens {
		if i == 0 || i < r.KeepLast || len(refs[g.Number]) > 0 || aliased[g.Number] {
			continue
		}
		if r.KeepDays > 0 && !g.CreatedAt.IsZero() && now.Sub(g.CreatedAt) < time.Duration(r.KeepDays)*24*time.Hour {
			continue
		}
		out = append(out, g.Number)
	}
	sort.Ints(out)
	return out
}

// Prune deletes the generations Candidates selects and returns their numbers.
func (m *Manager) Prune(ctx context.Context, image string, r api.ImageRetention, jails []JailRef) ([]int, error) {
	o, err := m.Observe(ctx, image)
	if err != nil {
		return nil, err
	}
	var done []int
	var errs []error
	for _, n := range Candidates(r, image, o.Status, jails, m.now()) {
		if _, err := m.Delete(ctx, image, n, jails, false); err != nil {
			errs = append(errs, err)
			continue
		}
		done = append(done, n)
	}
	if len(errs) > 0 {
		return done, errs[0]
	}
	return done, nil
}
