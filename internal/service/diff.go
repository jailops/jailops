package service

import (
	"context"
	"fmt"
	"strings"

	"jailops.net/jailops/internal/apps"
	"jailops.net/jailops/internal/lifecycle"
	"jailops.net/jailops/internal/sys/run"
	"jailops.net/jailops/sdk/api"
)

// JailDiff lists what a thin jail holds on top of its image: the files its root added, changed or removed since the
// image generation, and the same for each persistent dataset since the jail was made. limit is the number of entries
// listed per dataset (0 is api.DefaultDiffLimit); dataset names one dataset, "" all; packages adds the packages
// installed on purpose.
func (s *Service) JailDiff(ctx context.Context, name string, limit int, dataset string, packages bool) (api.JailDiff, error) {
	_, key, err := s.jailRef(ctx, name)
	if err != nil {
		return api.JailDiff{}, err
	}
	j, err := s.getByKey(ctx, key)
	if err != nil {
		return api.JailDiff{}, err
	}
	if s.life == nil {
		return api.JailDiff{}, api.Errorf(api.CodeUnsupported, "this daemon does not manage jails")
	}
	if limit < 0 || limit > api.MaxDiffLimit {
		return api.JailDiff{}, invalid("limit must be between 0 and %d", api.MaxDiffLimit)
	}
	if limit == 0 {
		limit = api.DefaultDiffLimit
	}
	if s.jailIsLinux(j) {
		return api.JailDiff{}, api.Errorf(api.CodeInvalid, "jail %q is a Linux jail: it has a full copy of its image, so there is nothing to compare it with", name).
			WithHint("jail diff needs a thin FreeBSD jail")
	}
	unlock, err := s.locks.lockPatient(ctx, "list the changes of "+key, jailLock(key))
	if err != nil {
		return api.JailDiff{}, err
	}
	r, err := s.life.DiffJail(ctx, key, limit)
	unlock()
	if err != nil {
		return api.JailDiff{}, err
	}
	out := api.JailDiff{Jail: name, Image: r.Image, Datasets: []api.DiffDataset{}}
	for _, d := range r.Datasets {
		if dataset != "" && d.Name != dataset {
			continue
		}
		out.Datasets = append(out.Datasets, diffDataset(d))
	}
	if dataset != "" && len(out.Datasets) == 0 {
		names := make([]string, len(r.Datasets))
		for i, d := range r.Datasets {
			names[i] = d.Name
		}
		return api.JailDiff{}, invalid("jail %q has no dataset %q to compare: %s", name, dataset, strings.Join(names, ", "))
	}
	if packages {
		out.Packages = s.diffPackages(ctx, j, key)
	}
	return out, nil
}

func diffDataset(d lifecycle.DatasetDiff) api.DiffDataset {
	out := api.DiffDataset{Name: d.Name, Path: d.Path, Base: d.Base, Note: d.Note, Total: d.Total, Truncated: d.Truncated,
		Entries: []api.DiffEntry{}, Counts: map[string]int{}}
	kind := map[string]string{"+": api.DiffAdded, "M": api.DiffModified, "-": api.DiffRemoved, "R": api.DiffRenamed}
	for k, n := range d.Counts {
		if w := kind[k]; w != "" && n > 0 {
			out.Counts[w] = n
		}
	}
	for _, e := range d.Entries {
		if w := kind[e.Kind]; w != "" {
			out.Entries = append(out.Entries, api.DiffEntry{Kind: w, Path: e.Path, To: e.To})
		}
	}
	return out
}

// diffPackages lists the packages installed on purpose. Only a running jail can say: pkg reads its database through the
// jail.
func (s *Service) diffPackages(ctx context.Context, j api.Jail, key string) *api.DiffPackages {
	out := &api.DiffPackages{Packages: []string{}}
	name := j.Metadata.Name
	if j.Status.Power != api.PowerRunning || s.d.Run == nil {
		out.Note = "the packages are listed for a running jail: jailops start " + name
		return out
	}
	s.execMu.Lock()
	installing := s.installing[key]
	s.execMu.Unlock()
	if installing {
		out.Note = "an install is working on the jail: try again when it has ended"
		return out
	}
	argv := apps.PkgLeavesArgv(key)
	res, err := s.d.Run.Run(ctx, run.Cmd{Name: argv[0], Args: argv[1:], Timeout: draftTimeout})
	if err != nil {
		out.Note = fmt.Sprintf("pkg could not list the packages: %v", err)
		return out
	}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		if j.Spec.Shell == api.BashPath && f[0] == api.BashPackage {
			continue
		}
		out.Packages = append(out.Packages, f[0]+"-"+f[1])
	}
	return out
}
