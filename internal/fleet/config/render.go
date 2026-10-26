package config

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/sdk/api"
)

// Render returns the resolved documents of one host as multi-document YAML:
// the objects exactly as sync would send them (templates merged, host
// references expanded). Secret references stay references; nothing is
// decrypted.
func (f *Fleet) Render(host string) ([]byte, error) {
	h, ok := f.Hosts[host]
	if !ok {
		return nil, fmt.Errorf("unknown host %q", host)
	}
	var buf bytes.Buffer
	emit := func(origin string, doc any) error {
		fmt.Fprintf(&buf, "---\n# %s\n", origin)
		b, err := yaml.Marshal(doc)
		if err != nil {
			return err
		}
		buf.Write(b)
		return nil
	}
	hostSpec, err := model.ToMap(h.Spec)
	if err != nil {
		return nil, err
	}
	if err := emit(h.Origin, api.Document{
		APIVersion: api.APIVersion, Kind: api.KindHost,
		Metadata: api.ObjectMeta{Name: h.Name, Labels: h.Labels}, Spec: hostSpec,
	}); err != nil {
		return nil, err
	}
	for _, o := range f.ObjectsFor(host) {
		spec, err := model.ToMap(o.Spec)
		if err != nil {
			return nil, err
		}
		doc := map[string]any{
			"apiVersion": api.APIVersion, "kind": string(o.Kind),
			"metadata": map[string]any{"name": o.Name, "managed_by": f.ManagedBy()},
			"spec":     spec,
		}
		if o.Project != "" {
			doc["metadata"].(map[string]any)["project"] = o.Project
		}
		if len(o.Labels) > 0 {
			doc["metadata"].(map[string]any)["labels"] = o.Labels
		}
		if o.Start {
			doc["start"] = true
		}
		if len(o.After) > 0 {
			doc["after"] = o.After
		}
		origin := o.Origin
		if len(o.Profiles) > 0 || len(o.Templates) > 0 {
			// The spec above is the expanded one; say where each key came from.
			keys := make([]string, 0, len(o.Provenance))
			for k := range o.Provenance {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			origin += "\n# expanded spec"
			if len(o.Profiles) > 0 {
				origin += " (profiles: " + strings.Join(o.Profiles, ", ") + ")"
			}
			origin += "; value sources:"
			for _, k := range keys {
				origin += fmt.Sprintf("\n#   %-10s %s", k, o.Provenance[k])
			}
		}
		if err := emit(origin, doc); err != nil {
			return nil, err
		}
	}
	return buf.Bytes(), nil
}
