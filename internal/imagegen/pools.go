package imagegen

import (
	"context"
	"fmt"
	"strconv"

	"jailops.net/jailops/internal/plan"
	"jailops.net/jailops/sdk/api"
)

// others returns the managers of the other pools.
func (m *Manager) others() []*Manager {
	if m.Peers == nil {
		return nil
	}
	var out []*Manager
	for _, p := range m.Peers() {
		if p != nil && p != m && p.Layout.Root != m.Layout.Root {
			out = append(out, p)
		}
	}
	return out
}

// pools returns every pool in preference order, this one included.
func (m *Manager) pools() []*Manager {
	if m.Peers != nil {
		l := m.Peers()
		for _, p := range l {
			if p == m {
				return l
			}
		}
	}
	return append([]*Manager{m}, m.others()...)
}

// MaxNumber is the highest generation number of image on any pool, parked
// generations included. Numbers are never reused, on any pool.
func (m *Manager) MaxNumber(ctx context.Context, image string) (int, error) {
	own, err := m.Observe(ctx, image)
	if err != nil {
		return 0, err
	}
	max := own.MaxNumber
	for _, p := range m.others() {
		o, err := p.Observe(ctx, image)
		if err != nil {
			return 0, fmt.Errorf("pool %s: %w", p.Layout.Root, err)
		}
		if o.MaxNumber > max {
			max = o.MaxNumber
		}
	}
	return max, nil
}

// findAcross finds generation n of image on another pool.
func (m *Manager) findAcross(ctx context.Context, image string, n int) (*holder, error) {
	for _, p := range m.others() {
		o, err := p.Observe(ctx, image)
		if err != nil {
			continue
		}
		if g, ok := o.gen(n); ok {
			return &holder{m: p, g: g, aliases: o.Status.Aliases}, nil
		}
	}
	return nil, api.Errorf(api.CodeNotFound, "generation %s does not exist", GenerationName(image, n))
}

type holder struct {
	m       *Manager
	g       api.ImageGeneration
	aliases map[string]int
}

// Holders lists the roots of the pools that hold generation n of image.
func (m *Manager) Holders(ctx context.Context, image string, n int) []string {
	var out []string
	for _, p := range append([]*Manager{m}, m.others()...) {
		if o, err := p.Observe(ctx, image); err == nil {
			if _, ok := o.gen(n); ok {
				out = append(out, p.Layout.Root)
			}
		}
	}
	return out
}

// Provide resolves a reference across every pool and makes sure this pool
// holds the generation it names, so that a jail on this pool can clone it:
//   - a pinned reference names its number; an alias is looked up here first,
//     then on the other pools; a bare image name means the newest generation
//     on any pool;
//   - a generation this pool already holds is returned as is;
//   - one that only another pool holds is received here (send and receive of
//     the immutable @ro snapshot, committed by a rename like a build);
//   - an image that has no generation on any pool is built here, from the
//     source.
func (m *Manager) Provide(ctx context.Context, ref string) (api.ImageGeneration, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return api.ImageGeneration{}, err
	}
	own, err := m.Observe(ctx, r.Image)
	if err != nil {
		return api.ImageGeneration{}, err
	}
	// The pools in preference order: the first pool that has an alias decides
	// what it means, so a rebuilt "stable" is followed on every pool.
	var obs []Observed
	var peerObs []holderObs
	for _, p := range m.pools() {
		if p == m {
			obs = append(obs, own)
			continue
		}
		o, err := p.Observe(ctx, r.Image)
		if err != nil {
			continue
		}
		obs = append(obs, o)
		peerObs = append(peerObs, holderObs{p, o})
	}
	n, anyGen := 0, false
	for _, o := range obs {
		if len(o.Status.Generations) > 0 {
			anyGen = true
		}
	}
	switch {
	case r.Gen > 0:
		n = r.Gen
	case r.Alias != "":
		for _, o := range obs {
			if v, ok := o.Status.Aliases[r.Alias]; ok {
				n = v
				break
			}
		}
		if n == 0 && anyGen {
			return api.ImageGeneration{}, api.Errorf(api.CodeNotFound, "image %s has no alias %q", r.Image, r.Alias)
		}
	default:
		for _, o := range obs {
			for _, g := range o.Status.Generations {
				if g.Number > n {
					n = g.Number
				}
			}
		}
	}
	if n == 0 {
		if r.Gen == 0 && !anyGen {
			res, err := m.Build(ctx, r.Image, api.ImageBuild{})
			if err != nil {
				return api.ImageGeneration{}, err
			}
			return res.Generation, nil
		}
		return api.ImageGeneration{}, api.Errorf(api.CodeNotFound, "image %s has no generations", r.Image)
	}
	if g, ok := own.gen(n); ok {
		return g, nil
	}
	for _, po := range peerObs {
		if g, ok := po.obs.gen(n); ok {
			return m.receive(ctx, r.Image, n, po.m, g, po.obs.Status.Aliases)
		}
	}
	return api.ImageGeneration{}, api.Errorf(api.CodeNotFound, "generation %s does not exist", GenerationName(r.Image, n))
}

type holderObs struct {
	m   *Manager
	obs Observed
}

// receive copies generation n of image from the pool of src into this pool.
func (m *Manager) receive(ctx context.Context, image string, n int, src *Manager, g api.ImageGeneration, srcAliases map[string]int) (api.ImageGeneration, error) {
	z := m.ZFS
	imageDS := m.Layout.ImageDS(image)
	staging := m.Layout.StagingDS(image, n)
	final := m.Layout.GenDS(image, n)
	srcRO := src.Layout.GenDS(image, n) + "@" + ROSnap
	stagingRO := staging + "@" + ROSnap
	finalMount := m.Layout.GenMount(image, n)
	imageMade := false
	var oldAlias, oldLast string
	var hadAlias bool
	destroy := func(ds string) func(context.Context) error {
		return func(ctx context.Context) error {
			ok, err := z.Exists(ctx, ds)
			if err != nil || !ok {
				return err
			}
			return z.DestroyRecursive(ctx, ds)
		}
	}
	p := plan.New("image-receive " + GenerationName(image, n))
	p.Add(plan.Step{
		Name: "ensure-image-dataset", Describe: "zfs create " + imageDS + " (if missing)",
		Do: func(ctx context.Context) error {
			ok, err := z.Exists(ctx, imageDS)
			if err != nil || ok {
				return err
			}
			imageMade = true
			return z.Create(ctx, imageDS, map[string]string{PropManaged: "on", PropImage: image, PropKind: KindImage, "canmount": "off", "mountpoint": "none"})
		},
		Undo: func(ctx context.Context) error {
			if !imageMade {
				return nil
			}
			if ok, _ := z.Exists(ctx, imageDS); !ok {
				return nil
			}
			return z.Destroy(ctx, imageDS)
		},
		UndoOnFail: true,
	})
	p.Add(plan.Step{
		Name: "receive", Describe: fmt.Sprintf("zfs send -p %s | zfs receive %s", srcRO, staging),
		Do: func(ctx context.Context) error {
			return z.Replicate(ctx, srcRO, staging, map[string]string{
				PropManaged: "on", PropImage: image, PropKind: KindGeneration, PropGen: strconv.Itoa(n),
				"canmount": "noauto", "mountpoint": finalMount})
		},
		Undo: destroy(staging), UndoOnFail: true,
	})
	p.Add(plan.Step{Name: "verify-ro", Describe: "require " + stagingRO,
		Do: func(ctx context.Context) error {
			if ok, err := z.Exists(ctx, stagingRO); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("the received generation has no @%s snapshot", ROSnap)
			}
			return nil
		}})
	p.Add(plan.Step{Name: "hold-ro", Describe: "zfs hold " + HoldTag + " " + stagingRO,
		Do: func(ctx context.Context) error { return z.Hold(ctx, stagingRO, HoldTag) },
		Undo: func(ctx context.Context) error {
			for _, snap := range []string{stagingRO, final + "@" + ROSnap} {
				if ok, _ := z.Exists(ctx, snap); !ok {
					continue
				}
				hs, err := z.Holds(ctx, snap)
				if err != nil {
					return err
				}
				for _, h := range hs {
					if h == HoldTag {
						return z.Release(ctx, snap, HoldTag)
					}
				}
			}
			return nil
		}})
	p.Add(plan.Step{Name: "rename-into-place", Describe: "zfs rename " + staging + " " + final + "  (commit point)",
		Do: func(ctx context.Context) error { return z.Rename(ctx, staging, final) },
		Undo: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, final); !ok {
				return nil
			}
			if ok, _ := z.Exists(ctx, staging); ok {
				return nil
			}
			return z.Rename(ctx, final, staging)
		}})
	p.Add(plan.Step{
		Name: "copy-aliases", Describe: "adopt the aliases of the source pool that this pool lacks",
		Do: func(ctx context.Context) error {
			old, err := z.Get(ctx, imageDS, PropAliases)
			if err != nil {
				return err
			}
			oldAlias, hadAlias = old, old != ""
			al := parseAliases(old)
			changed := false
			for a, num := range srcAliases {
				if _, set := al[a]; !set && num == n {
					al[a] = num
					changed = true
				}
			}
			if !changed {
				return nil
			}
			return z.Set(ctx, imageDS, map[string]string{PropAliases: formatAliases(al)})
		},
		Undo: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, imageDS); !ok {
				return nil
			}
			cur, _ := z.Get(ctx, imageDS, PropAliases)
			if cur == oldAlias {
				return nil
			}
			if hadAlias {
				return z.Set(ctx, imageDS, map[string]string{PropAliases: oldAlias})
			}
			return z.Inherit(ctx, imageDS, PropAliases)
		}, UndoOnFail: true})
	p.Add(plan.Step{
		Name: "record-last-generation", Describe: fmt.Sprintf("zfs set %s=%d %s", PropLastGen, n, imageDS),
		Do: func(ctx context.Context) error {
			oldLast, _ = z.Get(ctx, imageDS, PropLastGen)
			if v, err := strconv.Atoi(oldLast); err == nil && v >= n {
				return nil
			}
			return z.Set(ctx, imageDS, map[string]string{PropLastGen: strconv.Itoa(n)})
		},
		Undo: func(ctx context.Context) error {
			if ok, _ := z.Exists(ctx, imageDS); !ok {
				return nil
			}
			cur, _ := z.Get(ctx, imageDS, PropLastGen)
			if cur == oldLast {
				return nil
			}
			if oldLast == "" {
				return z.Inherit(ctx, imageDS, PropLastGen)
			}
			return z.Set(ctx, imageDS, map[string]string{PropLastGen: oldLast})
		}, UndoOnFail: true})
	if err := m.executor().Run(ctx, fmt.Sprintf("image-receive/%s/g%d", image, n), p); err != nil {
		return api.ImageGeneration{}, err
	}
	after, err := m.Observe(ctx, image)
	if err != nil {
		return api.ImageGeneration{}, err
	}
	got, ok := after.gen(n)
	if !ok {
		return api.ImageGeneration{}, api.Errorf(api.CodeInternal, "generation %s missing after receive", GenerationName(image, n))
	}
	return got, nil
}
