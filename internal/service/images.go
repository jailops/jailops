package service

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"

	"jailops.net/jailops/internal/imagegen"
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

// DeleteImage removes an image with all its generations. No jail depends on an image (a jail is a copy of a
// generation); a generation that still has ZFS clones is parked until they are gone (imagegen.Manager.Delete).
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
	for _, m := range s.imageManagers() {
		o, err := m.Observe(ctx, name)
		if err != nil {
			continue
		}
		for _, g := range o.Status.Generations {
			if _, err := m.Delete(ctx, name, g.Number, false); err != nil {
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
// UpgradeImage builds the next generation of an image from its newest one, with the packages of its repository at
// their newest versions, as one operation. No jail is touched: a jail is a copy of a generation and is upgraded in
// place (jail upgrade); the new generation is what new jails are copied from. When the package set did not change no
// generation is kept.
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
	mgr, err := s.buildPool(req.Storage)
	if err != nil {
		return api.Operation{}, err
	}
	from := s.newestGeneration(ctx, image)
	if from == 0 {
		return api.Operation{}, api.Errorf(api.CodeNotFound, "image %q has no generation to upgrade", image).
			WithHint("build the first one with 'jailops image build " + image + "'")
	}
	return s.Submit(ctx, OpSpec{
		Action: api.OpImageUpgrade, Description: "Upgrade image " + image, Locks: []string{imageLock(image)},
		Resources: map[string][]string{"images": {"/api/v1/images/" + image}},
		Run: func(ctx context.Context, h *OpHandle) (any, error) {
			return s.upgradeImageRun(ctx, h, mgr, image, from, req)
		},
	})
}

// newestGeneration is the number of the newest generation of an image on any pool, 0 when it has none.
func (s *Service) newestGeneration(ctx context.Context, image string) int {
	st, _, _ := s.observeAcross(ctx, image)
	from := 0
	for _, g := range st.Generations {
		from = max(from, g.Number)
	}
	return from
}

// upgradeImageRun is the body of the image upgrade operation: the build of the next generation from generation from.
func (s *Service) upgradeImageRun(ctx context.Context, h *OpHandle, mgr *imagegen.Manager, image string, from int, req api.ImageUpgrade) (api.ImageUpgradeResult, error) {
	out := api.ImageUpgradeResult{From: imagegen.GenerationName(image, from), DryRun: req.DryRun}
	res, err := s.buildBody(ctx, h, mgr, image, api.ImageBuild{FromGeneration: from, DryRun: req.DryRun, Storage: req.Storage}, true)
	if err != nil {
		return out, err
	}
	out.Plan, out.Unchanged = res.Plan, res.Unchanged
	if res.Generation.Number > 0 {
		g := res.Generation
		out.Generation = &g
	}
	if !req.DryRun && !res.Unchanged {
		s.emit(ctx, "image-upgraded", "/api/v1/images/"+image)
	}
	return out, nil
}

// DeleteGeneration deletes a generation on every pool that holds a copy, as an operation.
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
				if _, err := m.Delete(ctx, image, n, false); err != nil {
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
