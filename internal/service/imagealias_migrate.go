package service

import (
	"context"
	"fmt"
	"slices"

	"jailops.net/jailops/internal/state"
	"jailops.net/jailops/sdk/api"
)

// dropImageAliases is the one-off step after the removal of image aliases. The state store has already rewritten the stored
// jails that named an alias ("freebsd-15@stable") to the bare image name and dropped the alias lists of the images; this reads
// the alias table that is still on each image dataset, names in an event the generation each such jail's alias pointed at, and
// clears the table. A table that cannot be cleared is logged and tried again at the next start.
func (s *Service) dropImageAliases(ctx context.Context) {
	if s.d.State == nil {
		return
	}
	s.reportImageAliases(ctx, s.d.State.TakeMigratedImageRefs())
}

func (s *Service) reportImageAliases(ctx context.Context, refs []state.MigratedImageRef) {
	tables := map[string]map[string]int{}
	table := func(image string) map[string]int {
		if t, ok := tables[image]; ok {
			return t
		}
		t := map[string]int{}
		for _, m := range s.imageManagers() {
			got, err := m.DropAliasTable(ctx, image)
			if err != nil {
				s.log.Warn("clearing the stored alias table of an image failed", "image", image, "err", err)
				continue
			}
			for a, n := range got {
				t[a] = n
			}
		}
		tables[image] = t
		return t
	}
	// every image of the registry, and every image a jail named, loses its table
	var names []string
	if objs, err := s.imgKind.list(); err == nil {
		for _, o := range objs {
			names = append(names, o.Meta.Name)
		}
	}
	for _, r := range refs {
		names = append(names, r.Image)
	}
	slices.Sort(names)
	for _, n := range slices.Compact(names) {
		table(n)
	}
	for _, r := range refs {
		gen := "an unknown generation"
		if n, ok := table(r.Image)[r.Alias]; ok {
			gen = fmt.Sprintf("generation %d", n)
		}
		detail := fmt.Sprintf("jail %s used the image alias %s@%s, which pointed at %s; its image is now %s", r.Jail, r.Image, r.Alias, gen, r.Image)
		s.log.Info("image alias removed from a stored jail", "jail", r.Jail, "image", r.Image, "alias", r.Alias, "generation", gen)
		s.events.publish(api.EventLifecycle, api.LifecycleEvent{Action: "image-alias-removed", Source: "/api/v1/jails/" + r.Jail,
			Requestor: "jailops", Project: r.Project, Detail: detail}, s.now())
	}
}
