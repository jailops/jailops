package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/internal/imagegen"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/validate"
	"jailops.net/jailops/sdk/api"
)

// jailDisplay is how a jail is named in references: "<project>/<name>".
func jailDisplay(project, name string) string {
	if project == "" {
		return name
	}
	return project + "/" + name
}

// jailRefs lists the jails that reference images; keep selects the projects
// whose jails are listed (nil lists every jail).
func (s *Service) jailRefs(ctx context.Context, keep func(project string) bool) []imagegen.JailRef {
	s.mu.RLock()
	keys := sortedKeys(s.jails)
	var out []imagegen.JailRef
	var rootKeys []string
	for _, k := range keys {
		j := s.jails[k].jail
		if keep != nil && !keep(j.Metadata.Project) {
			continue
		}
		out = append(out, imagegen.JailRef{Name: jailDisplay(j.Metadata.Project, j.Metadata.Name), Image: j.Spec.Image, Current: j.Status.ImageGeneration})
		rootKeys = append(rootKeys, k)
	}
	s.mu.RUnlock()
	if s.d.ZFS != nil {
		for i := range out {
			ds := s.lcfg.RootDS(rootKeys[i])
			if g, _ := s.d.ZFS.Get(ctx, ds, lifecycle.PropPrevGen); g != "" {
				if img, _, ok := cutImage(out[i].Current); ok {
					if n, err := strconv.Atoi(g); err == nil {
						out[i].Previous = imagegen.GenerationName(img, n)
					}
				}
			}
		}
	}
	return out
}

func cutImage(gen string) (img, rest string, ok bool) {
	for i := 0; i < len(gen); i++ {
		if gen[i] == '#' {
			return gen[:i], gen[i+1:], true
		}
	}
	return "", "", false
}

// generationFreeBSD is the FreeBSD version of a generation named "<image>#g<N>" (what the jail list shows next to
// it), read from the first pool that holds it and cached.
func (s *Service) generationFreeBSD(ctx context.Context, gen string) string {
	r, err := imagegen.ParseRef(gen)
	if gen == "" || err != nil || r.Gen == 0 {
		return ""
	}
	s.genVerMu.Lock()
	v, ok := s.genVer[gen]
	s.genVerMu.Unlock()
	if ok {
		return v
	}
	for _, si := range s.storageList() {
		if si.Images == nil {
			continue
		}
		if v = si.Images.FreeBSDVersion(ctx, r.Image, r.Gen); v != "" {
			break
		}
	}
	s.genVerMu.Lock()
	if s.genVer == nil || len(s.genVer) >= maxGenVerCache {
		s.genVer = map[string]string{} // a cache: a generation that was deleted does not stay for ever
	}
	s.genVer[gen] = v
	s.genVerMu.Unlock()
	return v
}

// observeAcross reads an image on every pool and merges the generations by
// number (every pool that holds one is listed in Storages).
func (s *Service) observeAcross(ctx context.Context, image string) (api.ImageStatus, imagegen.Observed, bool) {
	var st api.ImageStatus
	var agg imagegen.Observed
	seen := map[int]int{}
	any := false
	for _, si := range s.storageList() {
		if si.Images == nil {
			continue
		}
		obs, err := si.Images.Observe(ctx, image)
		if err != nil {
			continue
		}
		any = true
		for _, g := range obs.Status.Generations {
			if idx, ok := seen[g.Number]; ok {
				st.Generations[idx].Storages = append(st.Generations[idx].Storages, si.Name)
				continue
			}
			g.Storages = []string{si.Name}
			seen[g.Number] = len(st.Generations)
			st.Generations = append(st.Generations, g)
		}
		agg.Deleted = append(agg.Deleted, obs.Deleted...)
		agg.Staging = append(agg.Staging, obs.Staging...)
		agg.Problems = append(agg.Problems, obs.Problems...)
		if obs.MaxNumber > agg.MaxNumber {
			agg.MaxNumber = obs.MaxNumber
		}
	}
	sort.Slice(st.Generations, func(i, j int) bool { return st.Generations[i].Number < st.Generations[j].Number })
	agg.Status = st
	return st, agg, any
}

func (s *Service) imageToAPI(ctx context.Context, o object[api.ImageSpec]) api.Image {
	img := api.Image{Metadata: o.Meta, Spec: o.Spec}
	if st, _, ok := s.observeAcross(ctx, o.Meta.Name); ok {
		img.Status = st
		var keep func(string) bool
		if a := ActorFrom(ctx); !a.Global {
			keep = a.CanProject
		}
		imagegen.ApplyRefs(o.Meta.Name, &img.Status, s.jailRefs(ctx, keep))
	}
	img.Status.AutoUpgrade = s.autoUpgradeStatus(o.Meta.Name, o.Spec)
	return img
}

// imageVisible says whether the caller may see an image: global identities
// see every image, whatever project the request names (an image is the host's), project identities the images their
// projects allow.
func (s *Service) imageVisible(ctx context.Context, name string) bool {
	if ActorFrom(ctx).Global {
		return true
	}
	return s.imageUsable(ctx, name)
}

// imageUsable says whether the project of the request, or the projects of the caller when it names none, may create
// a jail from an image: global identities with no project may use every image.
func (s *Service) imageUsable(ctx context.Context, name string) bool {
	a := ActorFrom(ctx)
	if a.Global && ProjectFrom(ctx) == "" {
		return true
	}
	projects := a.Projects
	if p := ProjectFrom(ctx); p != "" {
		if !a.CanProject(p) {
			return false
		}
		projects = []string{p}
	}
	for _, p := range projects {
		if o, err := s.projKind.get(p); err == nil && imageAllowed(o.Spec, name) {
			return true
		}
	}
	return false
}

// freebsdReleaseRE is a FreeBSD release with its type: 15.1-RELEASE, 15.2-PRERELEASE, 16.0-CURRENT, 15.1-RC2, with the
// patch level a host reports as an option (15.1-RELEASE-p3). Only a RELEASE reads the release repository of its minor;
// the other types read base_latest.
var freebsdReleaseRE = regexp.MustCompile(`^[0-9]{1,3}\.[0-9]{1,3}-(RELEASE|STABLE|CURRENT|PRERELEASE|(ALPHA|BETA|RC)[0-9]{1,2})(-p[0-9]{1,4})?$`)

func (s *Service) validateImage(name string, spec api.ImageSpec) error {
	if err := validate.Name("name", name); err != nil {
		return err
	}
	if spec.OS == "" || spec.Release == "" {
		return invalid("spec.os and spec.release are required")
	}
	if spec.OS != api.OSFreeBSD && spec.OS != api.OSLinux {
		return invalid("spec.os %q must be freebsd or linux", spec.OS)
	}
	if spec.OS == api.OSFreeBSD {
		var major, minor int
		if !freebsdReleaseRE.MatchString(spec.Release) {
			// a release without its type ("15.1") or a typo would be built from base_latest, silently: refuse it
			e := invalid("spec.release %q is not a FreeBSD release such as 15.1-RELEASE", spec.Release)
			if host := s.hostBaseRelease(); host != "" {
				e = e.WithHint("this host runs " + host + ": jailops image create " + api.ImageNameForRelease(host) + " --release " + host)
			}
			return e
		}
		if _, err := fmt.Sscanf(spec.Release, "%d.%d", &major, &minor); err != nil {
			return invalid("spec.release %q is not a FreeBSD release such as 15.1-RELEASE", spec.Release)
		}
		if major < minHostMajor || (major == minHostMajor && minor < minHostMinor) {
			return invalid("spec.release %q is older than the first release JailOps supports, 15.1", spec.Release)
		}
	}
	src := s.d.ImageSources
	if src == nil {
		src = imagegen.DefaultSources()
	}
	if spec.Source.Type == "" {
		return invalid("spec.source.type is required")
	}
	so, ok := src[spec.Source.Type]
	if !ok {
		return invalid("unknown image source %q", spec.Source.Type)
	}
	if err := so.CheckSpec(spec); err != nil {
		return err
	}
	if au := spec.AutoUpgrade; au != nil && au.At != "" {
		if _, err := parseAutoUpgradeAt(au.At); err != nil {
			return err
		}
	}
	return nil
}

// ListImages returns the images the caller may see with their generations.
func (s *Service) ListImages(ctx context.Context, labels map[string]string) ([]api.Image, error) {
	if !ActorFrom(ctx).HasScope() {
		return nil, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	objs, err := s.imgKind.list()
	if err != nil {
		return nil, err
	}
	out := []api.Image{}
	for _, o := range objs {
		if MatchLabels(o.Meta.Labels, labels) && s.imageVisible(ctx, o.Meta.Name) {
			out = append(out, s.imageToAPI(ctx, o))
		}
	}
	return out, nil
}

// GetImage returns one image; an image the caller's projects do not allow is not_found.
func (s *Service) GetImage(ctx context.Context, name string) (api.Image, error) {
	if !ActorFrom(ctx).HasScope() {
		return api.Image{}, api.Errorf(api.CodeForbidden, "this identity has no projects and is not global")
	}
	o, err := s.imgKind.get(name)
	if err != nil || !s.imageVisible(ctx, name) {
		return api.Image{}, notFound("image", name)
	}
	return s.imageToAPI(ctx, o), nil
}

// CreateImage stores an image definition. Generations are built separately.
func (s *Service) CreateImage(ctx context.Context, im api.Image) (api.Image, error) {
	if err := requireGlobal(ctx, "creating an image"); err != nil {
		return api.Image{}, err
	}
	if err := s.imgKind.notTaken(im.Metadata.Name); err != nil {
		return api.Image{}, err
	}
	if err := validate.NameLength("image", "name", im.Metadata.Name, validate.MaxImageName); err != nil {
		return api.Image{}, err
	}
	if err := s.validateImage(im.Metadata.Name, im.Spec); err != nil {
		return api.Image{}, err
	}
	o, err := s.imgKind.create(ctx, im.Metadata, im.Spec)
	if err != nil {
		return api.Image{}, err
	}
	return s.imageToAPI(ctx, o), nil
}

// UpdateImage replaces an image definition.
func (s *Service) UpdateImage(ctx context.Context, name string, im api.Image, expect int64) (api.Image, error) {
	if err := requireGlobal(ctx, "changing an image"); err != nil {
		return api.Image{}, err
	}
	if im.Metadata.Name != "" && im.Metadata.Name != name {
		return api.Image{}, renameByAction("image", "images")
	}
	if err := s.validateImage(name, im.Spec); err != nil {
		return api.Image{}, err
	}
	unlock, err := s.locks.lockPatient(ctx, "update image "+name, imageLock(name))
	if err != nil {
		return api.Image{}, err
	}
	defer unlock()
	o, err := s.imgKind.update(ctx, name, expect, im.Metadata.Labels, &im.Metadata.ManagedBy, im.Spec)
	if err != nil {
		return api.Image{}, err
	}
	return s.imageToAPI(ctx, o), nil
}

// PatchImage applies a merge patch.
func (s *Service) PatchImage(ctx context.Context, name string, patch map[string]any, expect int64) (api.Image, error) {
	if err := requireGlobal(ctx, "changing an image"); err != nil {
		return api.Image{}, err
	}
	cur, err := s.GetImage(ctx, name)
	if err != nil {
		return api.Image{}, err
	}
	if expect != AnyGeneration && expect != cur.Metadata.Generation {
		return api.Image{}, api.Errorf(api.CodePreconditionFailed, "image %q changed: current generation is %d", name, cur.Metadata.Generation)
	}
	var out api.Image
	if err := MergePatch(cur, patch, &out); err != nil {
		return api.Image{}, err
	}
	return s.UpdateImage(ctx, name, out, cur.Metadata.Generation)
}

// DeleteImage removes an image with all its generations. It is refused, naming the jails, while any jail
// references the image.
func (s *Service) DeleteImage(ctx context.Context, name string, expect int64) error {
	if err := requireGlobal(ctx, "deleting an image"); err != nil {
		return err
	}
	unlock, err := s.locks.lockPatient(ctx, "delete image "+name, imageLock(name))
	if err != nil {
		return err
	}
	defer unlock()
	if !s.imgKind.exists(name) {
		return notFound("image", name)
	}
	var users []string
	s.mu.RLock()
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		if ref, err := validate.ParseImageRef(j.Spec.Image); err == nil && ref.Name == name {
			users = append(users, jailDisplay(j.Metadata.Project, j.Metadata.Name))
		}
	}
	s.mu.RUnlock()
	if len(users) > 0 {
		return api.Errorf(api.CodeImageInUse, "image %q is used by jail %s", name, strings.Join(users, ", ")).
			WithDetail("jails", strings.Join(users, ",")).WithHint("upgrade or destroy those jails first")
	}
	refs := s.jailRefs(ctx, nil)
	for _, m := range s.imageManagers() {
		o, err := m.Observe(ctx, name)
		if err != nil {
			continue
		}
		for _, g := range o.Status.Generations {
			if _, err := m.Delete(ctx, name, g.Number, refs, false); err != nil {
				return err
			}
		}
		if _, err := m.Collect(ctx); err != nil {
			s.log.Warn("collecting deleted generations failed", "image", name, "err", err)
		}
		if err := m.DeleteImage(ctx, name); err != nil {
			return err
		}
	}
	return s.imgKind.delete(ctx, name, expect)
}

// ListGenerations returns the generations of an image with their references.
func (s *Service) ListGenerations(ctx context.Context, image string) ([]api.ImageGeneration, error) {
	im, err := s.GetImage(ctx, image)
	if err != nil {
		return nil, err
	}
	if im.Status.Generations == nil {
		return []api.ImageGeneration{}, nil
	}
	return im.Status.Generations, nil
}

// GetGeneration returns one generation.
func (s *Service) GetGeneration(ctx context.Context, image string, n int) (api.ImageGeneration, error) {
	gens, err := s.ListGenerations(ctx, image)
	if err != nil {
		return api.ImageGeneration{}, err
	}
	for _, g := range gens {
		if g.Number == n {
			return g, nil
		}
	}
	return api.ImageGeneration{}, notFound("generation", strconv.Itoa(n))
}

// BuildResult is the operation result of a generation build. The embedded
// generation decodes as api.ImageGeneration on the client; Plan lists the
// steps (all there is for a dry run).
type BuildResult struct {
	api.ImageGeneration
	Plan   []string `json:"plan,omitempty"`
	DryRun bool     `json:"dry_run,omitempty"`
}

// BuildGeneration starts building the next generation as an operation. The
// generation is built on the pool of req.Storage (the default storage when
// empty); other pools receive it when a jail there needs it.
func (s *Service) BuildGeneration(ctx context.Context, image string, req api.ImageBuild) (api.Operation, error) {
	if err := requireGlobal(ctx, "building an image generation"); err != nil {
		return api.Operation{}, err
	}
	if err := s.requireHost("image builds"); err != nil {
		return api.Operation{}, err
	}
	if !s.imgKind.exists(image) {
		return api.Operation{}, notFound("image", image)
	}
	if req.FromGeneration < 0 {
		return api.Operation{}, invalid("from_generation must not be negative")
	}
	mgr, err := s.buildPool(req.Storage)
	if err != nil {
		return api.Operation{}, err
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpImageBuild, Description: "Build image " + image, Locks: []string{imageLock(image)},
		Resources: map[string][]string{"images": {"/api/v1/images/" + image}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			res, err := s.buildBody(ctx, h, mgr, image, req, false)
			if err != nil {
				return nil, err
			}
			return BuildResult{ImageGeneration: res.Generation, Plan: res.Plan, DryRun: res.DryRun}, nil
		},
	})
}

// buildPool is the image pool a build goes to: the named storage, else the default.
func (s *Service) buildPool(storage string) (*imagegen.Manager, error) {
	mgr := s.defaultImages()
	if storage != "" {
		si, ok := s.storage(storage)
		if !ok {
			return nil, notFound("storage", storage)
		}
		mgr = si.Images
	}
	if mgr == nil {
		return nil, api.Errorf(api.CodeUnsupported, "image builds need a storage")
	}
	return mgr, nil
}

// buildBody is the build inside an operation: the host pass a Linux image
// needs, the build itself, then the aliases of the spec follow the new
// generation and the event is published. upgrade keeps the result only when
// the package set changed (imagegen.Manager.Upgrade).
func (s *Service) buildBody(ctx context.Context, h *OpHandle, mgr *imagegen.Manager, image string, req api.ImageBuild, upgrade bool) (imagegen.BuildResult, error) {
	if max, err := mgr.MaxNumber(ctx, image); err == nil && !req.DryRun {
		_ = h.Bind("image-build/"+image+"/g"+strconv.Itoa(max+1), nil)
	}
	if spec, err := s.imageSpec(ctx, image); err == nil && spec.OS == api.OSLinux {
		s.linuxBuilds.Add(1)
		defer s.linuxBuilds.Add(-1)
	}
	if err := s.ensureLinuxHost(ctx, image); err != nil {
		return imagegen.BuildResult{}, err
	}
	var res imagegen.BuildResult
	var err error
	if upgrade {
		res, err = mgr.Upgrade(ctx, image, req)
	} else {
		res, err = mgr.Build(ctx, image, req)
	}
	if err != nil || req.DryRun || res.Unchanged {
		return res, err
	}
	s.emit(ctx, "image-generation-created", "/api/v1/images/"+image+"/generations/"+strconv.Itoa(res.Generation.Number))
	return res, nil
}

// imageUpgradeTarget is one jail that uses an image, with the reason it is
// not moved (empty for a jail that is).
type imageUpgradeTarget struct {
	key  string
	jail api.Jail
	skip string
}

// imageUpgradeTargets lists the jails that use an image in the projects the
// caller may use (one project when named), in key order. A thick jail (every
// Linux jail is one) is updated in place, not rebased; a strategy other than
// rebase and a spec that pins a generation are the user's choice and are
// left alone.
func (s *Service) imageUpgradeTargets(ctx context.Context, image, project string) []imageUpgradeTarget {
	a := ActorFrom(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []imageUpgradeTarget
	for _, k := range sortedKeys(s.jails) {
		j := s.jails[k].jail
		ref, err := validate.ParseImageRef(j.Spec.Image)
		if err != nil || ref.Name != image || !a.CanProject(j.Metadata.Project) || project != "" && j.Metadata.Project != project {
			continue
		}
		t := imageUpgradeTarget{key: k, jail: cloneJail(j)}
		switch {
		case j.Spec.OS == api.OSLinux:
			t.skip = "a Linux jail is thick and is updated in place"
		case j.Spec.Layout == api.LayoutThick:
			t.skip = "a thick jail is updated in place"
		case ref.Pinned:
			t.skip = "pinned to " + j.Spec.Image
		case len(s.jails[k].locked) > 0:
			t.skip = "locked: its encrypted volumes wait for a passphrase (jailops volume unlock)"
		}
		out = append(out, t)
	}
	return out
}

// UpgradeImage builds the next generation of an image from its newest one
// and moves the thin jails that use the image to it, one at a time, as one
// operation. Each move is the OS update of the update pipeline (snapshot, new
// root, /etc merge, swap, health check, automatic root rollback, a history
// record and the update events). A jail that fails or needs a human stops
// the operation before the next jail: a bad generation is not rolled across
// the host. A stopped jail is moved like a running one (started for the
// health check, then stopped again). When the package set did not change no
// generation is kept and no jail is touched.
func (s *Service) UpgradeImage(ctx context.Context, image string, req api.ImageUpgrade) (api.Operation, error) {
	if err := requireGlobal(ctx, "upgrading an image"); err != nil {
		return api.Operation{}, err
	}
	if err := s.requireHost("image upgrades"); err != nil {
		return api.Operation{}, err
	}
	if !s.imgKind.exists(image) {
		return api.Operation{}, notFound("image", image)
	}
	if req.Project != "" {
		if _, err := s.projKind.get(req.Project); err != nil || !ActorFrom(ctx).CanProject(req.Project) {
			return api.Operation{}, notFound("project", req.Project)
		}
	}
	if req.To != "" {
		return s.moveImageJails(ctx, image, req)
	}
	mgr, err := s.buildPool(req.Storage)
	if err != nil {
		return api.Operation{}, err
	}
	st, _, _ := s.observeAcross(ctx, image)
	from := 0
	for _, g := range st.Generations {
		from = max(from, g.Number)
	}
	if from == 0 {
		return api.Operation{}, api.Errorf(api.CodeNotFound, "image %q has no generation to upgrade", image).
			WithHint("build the first one with 'jailops image build " + image + "'")
	}
	targets := s.imageUpgradeTargets(ctx, image, req.Project)
	resources := map[string][]string{"images": {"/api/v1/images/" + image}}
	for _, t := range targets {
		if t.skip == "" {
			resources["jails"] = append(resources["jails"], "/api/v1/jails/"+t.jail.Metadata.Name)
		}
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpImageUpgrade, Description: "Upgrade image " + image, Locks: []string{imageLock(image)}, Resources: resources,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			return s.upgradeImageRun(ctx, h, mgr, image, from, req, targets, false)
		},
	})
}

// moveImageJails moves the thin jails that use an image to the newest generation of another image (a new release),
// one at a time, as one operation: every move is a `jail upgrade --image` (snapshot, new root, /etc merge, swap,
// health check, root rollback, history record). The first jail that fails stops it. Nothing is built: the other
// image has a generation already. A dry run checks every jail and changes nothing.
func (s *Service) moveImageJails(ctx context.Context, image string, req api.ImageUpgrade) (api.Operation, error) {
	to := strings.TrimSpace(req.To)
	ref, err := validate.ParseImageRef(to)
	switch {
	case err != nil:
		return api.Operation{}, err
	case ref.Pinned:
		return api.Operation{}, invalid("to names an image, not a generation: %q", to)
	case ref.Name == image:
		return api.Operation{}, invalid("to names the image itself: leave it out to build the next generation")
	case req.NoJails:
		return api.Operation{}, invalid("no_jails and to cannot be combined: moving the jails is all a move to another image does")
	case !s.imgKind.exists(ref.Name):
		return api.Operation{}, notFound("image", ref.Name)
	}
	st, _, _ := s.observeAcross(ctx, ref.Name)
	newest := api.ImageGeneration{}
	for _, g := range st.Generations {
		if g.Number > newest.Number {
			newest = g
		}
	}
	if newest.Number == 0 {
		return api.Operation{}, api.Errorf(api.CodeNotFound, "image %q has no generation to move the jails to", ref.Name).
			WithHint("build the first one with 'jailops image build " + ref.Name + "'")
	}
	targets := s.imageUpgradeTargets(ctx, image, req.Project)
	resources := map[string][]string{"images": {"/api/v1/images/" + image, "/api/v1/images/" + ref.Name}}
	for _, t := range targets {
		if t.skip == "" {
			resources["jails"] = append(resources["jails"], "/api/v1/jails/"+t.jail.Metadata.Name)
		}
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpImageMove, Description: "Move the jails of image " + image + " to " + ref.Name, Locks: []string{imageLock(image), imageLock(ref.Name)}, Resources: resources,
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			out := api.ImageUpgradeResult{From: imagegen.GenerationName(image, 0), DryRun: req.DryRun, Jails: []api.ImageUpgradeJail{}}
			g := newest
			out.Generation = &g
			stopped := ""
			for i, t := range targets {
				j := api.ImageUpgradeJail{Jail: t.jail.Metadata.Name, Project: t.jail.Metadata.Project, From: t.jail.Status.ImageGeneration}
				switch {
				case t.skip != "":
					j.Status, j.Reason = api.ImageUpgradeSkipped, t.skip
				case stopped != "":
					j.Status, j.Reason = api.ImageUpgradeNotAttempted, "stopped after "+stopped+" failed"
				default:
					j = s.moveJailToRelease(ctx, h, t, ref.Name, req.DryRun)
					if j.Status == api.ImageUpgradeFailed && !req.DryRun {
						stopped = jailDisplay(j.Project, j.Jail)
					}
				}
				out.Jails = append(out.Jails, j)
				h.Progress((i + 1) * 100 / (len(targets) + 1))
			}
			moved := false
			for _, j := range out.Jails {
				moved = moved || j.Status == api.ImageUpgradeUpgraded
			}
			if !req.DryRun && moved {
				s.emit(ctx, "image-upgraded", "/api/v1/images/"+image)
			}
			return out, nil
		},
	})
}

// moveJailToRelease moves one jail of a move of an image to another image, under the jail's locks, and reports the outcome. A
// dry run plans the move and says why it would fail.
func (s *Service) moveJailToRelease(ctx context.Context, h *OpHandle, t imageUpgradeTarget, image string, dry bool) api.ImageUpgradeJail {
	j := api.ImageUpgradeJail{Jail: t.jail.Metadata.Name, Project: t.jail.Metadata.Project, From: t.jail.Status.ImageGeneration}
	unlock, err := s.locks.lockPatient(ctx, "move jail "+t.key+" to image "+image, jailLock(t.key), anchorLock(t.key))
	if err != nil {
		j.Status, j.Reason = api.ImageUpgradeFailed, err.Error()
		return j
	}
	defer unlock()
	up := api.JailUpgrade{Image: image, DryRun: dry}
	req := lifecycle.UpgradeRequest{JailUpgrade: up, Stamp: s.stamp()}
	_, res, rec, err := s.moveRun(ctx, h, t.jail.Metadata.Project, t.jail.Metadata.Name, t.key, up, req)
	j.Record = rec
	if res != nil {
		j.From, j.To = res.From, res.To
	}
	switch {
	case err != nil:
		j.Status, j.Reason = api.ImageUpgradeFailed, err.Error()
	case res == nil || !res.Changed:
		j.Status, j.To, j.Reason = api.ImageUpgradeSkipped, "", "already on "+image
	case dry:
		j.Status = api.ImageUpgradePlanned
	default:
		j.Status = api.ImageUpgradeUpgraded
	}
	return j
}

// upgradeImageRun is the body of the image upgrade operation: the build of the next generation from generation
// `from`, then the move of every target jail.
func (s *Service) upgradeImageRun(ctx context.Context, h *OpHandle, mgr *imagegen.Manager, image string, from int, req api.ImageUpgrade, targets []imageUpgradeTarget, scheduled bool) (api.ImageUpgradeResult, error) {
	out := api.ImageUpgradeResult{From: imagegen.GenerationName(image, from), DryRun: req.DryRun, Jails: []api.ImageUpgradeJail{}}
	res, err := s.buildBody(ctx, h, mgr, image, api.ImageBuild{FromGeneration: from, DryRun: req.DryRun, Storage: req.Storage}, true)
	if err != nil {
		return out, err
	}
	out.Plan, out.Unchanged = res.Plan, res.Unchanged
	if res.Generation.Number > 0 {
		g := res.Generation
		out.Generation = &g
	}
	stopped := ""
	for i, t := range targets {
		j := api.ImageUpgradeJail{Jail: t.jail.Metadata.Name, Project: t.jail.Metadata.Project, From: t.jail.Status.ImageGeneration}
		switch {
		case t.skip != "":
			j.Status, j.Reason = api.ImageUpgradeSkipped, t.skip
		case res.Unchanged && t.jail.Status.ImageGeneration == imagegen.GenerationName(image, from):
			j.Status, j.Reason = api.ImageUpgradeSkipped, "already on the newest generation"
		case res.Unchanged && req.DryRun:
			j.Status, j.To = api.ImageUpgradePlanned, imagegen.GenerationName(image, from)
		case res.Unchanged && req.NoJails:
			j.Status, j.Reason = api.ImageUpgradeNotAttempted, "no_jails was set"
		case res.Unchanged && stopped != "":
			j.Status, j.Reason = api.ImageUpgradeNotAttempted, "stopped after "+stopped+" failed"
		case res.Unchanged:
			// nothing new was built, but this jail is still on an older generation (an earlier run stopped before it)
			j = s.moveJailToImage(ctx, h, t, imagegen.GenerationName(image, from), scheduled)
			if j.Status == api.ImageUpgradeFailed {
				stopped = jailDisplay(j.Project, j.Jail)
			}
		case req.DryRun:
			j.Status, j.To = api.ImageUpgradePlanned, imagegen.GenerationName(image, from+1)
		case req.NoJails:
			j.Status, j.Reason = api.ImageUpgradeNotAttempted, "no_jails was set"
		case stopped != "":
			j.Status, j.Reason = api.ImageUpgradeNotAttempted, "stopped after "+stopped+" failed"
		default:
			j = s.moveJailToImage(ctx, h, t, res.Generation.Name, scheduled)
			if j.Status == api.ImageUpgradeFailed {
				stopped = jailDisplay(j.Project, j.Jail)
			}
		}
		out.Jails = append(out.Jails, j)
		h.Progress((i + 1) * 100 / (len(targets) + 1))
	}
	moved := false
	for _, j := range out.Jails {
		moved = moved || j.Status == api.ImageUpgradeUpgraded
	}
	if !req.DryRun && (!res.Unchanged || moved) {
		s.emit(ctx, "image-upgraded", "/api/v1/images/"+image)
	}
	return out, nil
}

// moveJailToImage moves one jail of an image upgrade through the OS update of
// the update pipeline, under the jail's locks, and reports the outcome. scheduled marks the history record as the
// work of an automatic upgrade.
func (s *Service) moveJailToImage(ctx context.Context, h *OpHandle, t imageUpgradeTarget, target string, scheduled bool) api.ImageUpgradeJail {
	j := api.ImageUpgradeJail{Jail: t.jail.Metadata.Name, Project: t.jail.Metadata.Project, From: t.jail.Status.ImageGeneration, To: target}
	unlock, err := s.locks.lockPatient(ctx, "upgrade image for jail "+t.key, jailLock(t.key), anchorLock(t.key))
	if err != nil {
		j.Status, j.Reason = api.ImageUpgradeFailed, err.Error()
		return j
	}
	recs := s.applyJail(ctx, h, t.key, applyWhat{os: true}, scheduled)
	unlock()
	if len(recs) == 0 {
		// Nothing to apply: the jail is gone, or its reference does not resolve to the new generation.
		if cur, err := s.jailSnapshot(t.key); err == nil {
			j.From = cur.Status.ImageGeneration
		}
		j.Status, j.To, j.Reason = api.ImageUpgradeSkipped, "", "already on "+j.From
		return j
	}
	rec := recs[0]
	j.From, j.To, j.Record = rec.From, rec.To, rec.ID
	if rec.Result == api.UpdateSuccess {
		j.Status = api.ImageUpgradeUpgraded
		return j
	}
	j.Status, j.Reason = api.ImageUpgradeFailed, rec.Error
	if rec.Note != "" {
		j.Reason += " (" + rec.Note + ")"
	}
	return j
}

// DeleteGeneration deletes a generation no jail references, on every pool that
// holds a copy, as an operation. Aliases that pointed at it move to the next newest
// remaining generation, or are removed when none remains.
func (s *Service) DeleteGeneration(ctx context.Context, image string, n int) (api.Operation, error) {
	if err := requireGlobal(ctx, "deleting an image generation"); err != nil {
		return api.Operation{}, err
	}
	if err := s.requireHost("image generations"); err != nil {
		return api.Operation{}, err
	}
	g, err := s.GetGeneration(ctx, image, n)
	if err != nil {
		return api.Operation{}, err
	}
	if len(g.Refs) > 0 {
		return api.Operation{}, api.Errorf(api.CodeImageInUse, "generation %s is used by jail %s", g.Name, strings.Join(g.Refs, ", ")).
			WithDetail("jails", strings.Join(g.Refs, ",")).WithHint("upgrade or destroy those jails first")
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpImageGenerationDelete, Description: "Delete generation " + g.Name, Locks: []string{imageLock(image)},
		Resources: map[string][]string{"images": {"/api/v1/images/" + image + "/generations/" + strconv.Itoa(n)}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			var firstErr error
			deleted := false
			for _, m := range s.imageManagers() {
				if o, err := m.Observe(ctx, image); err != nil {
					continue
				} else if _, ok := genOf(o, n); !ok {
					continue
				}
				if _, err := m.Delete(ctx, image, n, s.jailRefs(ctx, nil), false); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					continue
				}
				deleted = true
			}
			if deleted {
				s.emit(ctx, "image-generation-deleted", "/api/v1/images/"+image+"/generations/"+strconv.Itoa(n))
			}
			return nil, firstErr
		},
	})
}

func genOf(o imagegen.Observed, n int) (api.ImageGeneration, bool) {
	for _, g := range o.Status.Generations {
		if g.Number == n {
			return g, true
		}
	}
	return api.ImageGeneration{}, false
}

// ensureLinuxHost loads the Linux modules and sets the fallback brand, as the host pass does for a Linux jail, before a
// Linux image is built: the build runs Linux binaries (apk, debootstrap's chroot) and fails with exec errors without
// them. It goes through the reconciler's host pass, so the same helper operations are used and nothing is widened.
func (s *Service) ensureLinuxHost(ctx context.Context, image string) error {
	spec, err := s.imageSpec(ctx, image)
	if err != nil || spec.OS != api.OSLinux || s.d.Reconciler == nil {
		return nil
	}
	if err := s.d.Reconciler.AssertHost(ctx, s.hostDesired()); err != nil {
		return fmt.Errorf("the host is not ready for a Linux image build: %w", err)
	}
	return nil
}

// maxGenVerCache bounds the cache of the FreeBSD versions of generations.
const maxGenVerCache = 4096
