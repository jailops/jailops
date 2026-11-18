package state

import (
	"strings"

	"jailops.net/jailops/sdk/api"
)

// MigratedImageRef is a jail that referred to its image by an alias ("freebsd-15@stable"). Image aliases are gone: the load
// rewrites the reference to the bare image name, and the service says in an event which alias the jail had used.
type MigratedImageRef struct {
	Project string
	Jail    string
	Image   string
	Alias   string
}

// splitAlias splits "image@alias". A reference without an alias returns it unchanged and an empty alias.
func splitAlias(ref string) (image, alias string) {
	image, alias, _ = strings.Cut(ref, "@")
	return image, alias
}

// stripImageAliases rewrites the image references of a stored object to the bare image name and drops the alias list of an
// image. One-off rewrite of objects written before image aliases were removed. It reports whether it changed doc.
func stripImageAliases(doc map[string]any) (out []MigratedImageRef, changed bool) {
	kind, _ := doc["kind"].(string)
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return nil, false
	}
	meta, _ := doc["metadata"].(map[string]any)
	project, _ := meta["project"].(string)
	name, _ := meta["name"].(string)
	switch api.Kind(kind) {
	case api.KindImage:
		if _, has := spec["aliases"]; has {
			delete(spec, "aliases")
			changed = true
		}
	case api.KindJail:
		if r := stripSpecImage(spec, project, name); r != nil {
			out, changed = append(out, *r), true
		}
	case api.KindBackup:
		bundle, _ := spec["bundle"].(map[string]any)
		inner, _ := bundle["spec"].(map[string]any)
		if inner != nil {
			if r := stripSpecImage(inner, project, name); r != nil {
				out, changed = append(out, *r), true
			}
		}
	case api.KindProject:
		imgs, _ := spec["images"].([]any)
		for i, x := range imgs {
			if s, ok := x.(string); ok {
				if img, al := splitAlias(s); al != "" {
					imgs[i] = img
					changed = true
				}
			}
		}
	}
	return out, changed
}

func stripSpecImage(spec map[string]any, project, jail string) *MigratedImageRef {
	ref, _ := spec["image"].(string)
	img, al := splitAlias(ref)
	if al == "" {
		return nil
	}
	spec["image"] = img
	return &MigratedImageRef{Project: project, Jail: jail, Image: img, Alias: al}
}

// TakeMigratedImageRefs returns the alias references the load rewrote since the last call.
func (s *Store) TakeMigratedImageRefs() []MigratedImageRef {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.imageRefs
	s.imageRefs = nil
	return out
}
