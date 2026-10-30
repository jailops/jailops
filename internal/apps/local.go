package apps

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"jailops.net/jailops/sdk/api"
)

// DefaultLocalDir is where an operator puts definitions of their own
// (apps.local_dir): read in addition to the packaged catalog, and winning
// over a packaged definition of the same name.
const DefaultLocalDir = "/usr/local/etc/jailops/apps"

// LoadAll reads the packaged catalog and the local directory and merges
// them by name: a local definition, valid or not, hides the packaged one of
// the same name and says so in Overrides. Each directory is read as Load
// reads it. An error names the directory that could not be read.
func LoadAll(catalog, local string) ([]Entry, error) {
	cat, err := Load(catalog)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", catalog, err)
	}
	var loc []Entry
	if local != "" && local != catalog {
		if loc, err = Load(local); err != nil {
			return nil, fmt.Errorf("%s: %w", local, err)
		}
	}
	byName := map[string]int{}
	out := make([]Entry, 0, len(cat)+len(loc))
	for _, e := range cat {
		e.Source = api.AppSourceCatalog
		byName[e.Name] = len(out)
		out = append(out, e)
	}
	for _, e := range loc {
		e.Source = api.AppSourceLocal
		if i, ok := byName[e.Name]; ok {
			e.Overrides = api.AppSourceCatalog
			out[i] = e
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// FindAll reads the definition of one name with its icon: the local one
// when the local directory has it, else the packaged one.
func FindAll(catalog, local, name string) (Entry, bool, error) {
	var e Entry
	found := false
	if local != "" && local != catalog {
		var err error
		if e, found, err = Find(local, name); err != nil {
			return Entry{}, false, err
		}
		if found {
			e.Source = api.AppSourceLocal
			if _, err := os.Lstat(filepath.Join(catalog, name+".yaml")); err == nil {
				e.Overrides = api.AppSourceCatalog
			}
			return e, true, nil
		}
	}
	e, found, err := Find(catalog, name)
	if found {
		e.Source = api.AppSourceCatalog
	}
	return e, found, err
}
