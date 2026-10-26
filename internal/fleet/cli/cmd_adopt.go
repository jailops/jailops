package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"jailops.net/jailops/internal/fleet/config"
	"jailops.net/jailops/internal/fleet/model"
	"jailops.net/jailops/internal/fleet/plan"
	"jailops.net/jailops/sdk/api"
)

func (a *app) cmdAdopt() *cobra.Command {
	var hosts []string
	var claim, overwrite bool
	var out string
	c := &cobra.Command{
		Use:   "adopt",
		Short: "Write live objects into the repository as YAML files",
		Long: `Reads the live objects of the selected hosts and writes each one as
hosts/<host>/<kind-dir>/<name>.yaml, so an existing host can be brought under
the repository. adopt never changes a host unless --claim is given.

Objects the repository already declares for that host are skipped, and so are
objects owned by another fleet. Existing files are kept unless --overwrite.

--claim then marks the adopted objects managed_by: sync:<fleet-id> on the
host (a merge patch guarded by the object's ETag), which is the deliberate
step that lets later syncs update them. Without it the first sync refuses
them, because it does not own them.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			f, err := a.load()
			if err != nil {
				return err
			}
			hs, err := hostsOf(f, hosts)
			if err != nil {
				return err
			}
			if out == "" {
				out = f.Path
			}
			s := a.session(f)
			defer s.Close()
			w := cmd.OutOrStdout()
			me := f.ManagedBy()
			total := 0
			for _, h := range hs {
				cl, err := s.Client(cmd.Context(), h)
				if err != nil {
					return fmt.Errorf("host %s: %w", h, err)
				}
				declared := map[config.Key]*config.Object{}
				for _, o := range f.ObjectsFor(h) {
					declared[o.Key()] = o
				}
				var projects []string
				if cl.Has("projects") {
					for pr, err := range cl.Projects().List(cmd.Context()) {
						if err != nil {
							return fmt.Errorf("host %s: list projects: %w", h, err)
						}
						projects = append(projects, pr.Metadata.Name)
					}
				}
				for _, ki := range model.Kinds {
					if !cl.Has(ki.Extension) {
						continue
					}
					ad, _ := plan.AdapterFor(ki.Kind)
					scopes := []string{""}
					if ki.ProjectLevel {
						scopes = projects
					}
					for _, project := range scopes {
						sc := plan.ScopeClient(cl, ki.Kind, project)
						live, err := ad.List(cmd.Context(), sc)
						if err != nil {
							return fmt.Errorf("host %s: list %s: %w", h, ki.Dir, err)
						}
						for _, l := range live {
							name := l.Meta.Name
							key := config.Key{Kind: ki.Kind, Project: project, Name: name}
							ref := plan.Item{Kind: ki.Kind, Project: project, Name: name}.Ref()
							if name == "default" && ki.Kind == api.KindStorage && l.Meta.ManagedBy == "" {
								continue // host-owned built-ins
							}
							if l.Meta.ManagedBy != "" && l.Meta.ManagedBy != me {
								fmt.Fprintf(w, "skip   %s %s: managed_by %q\n", h, ref, l.Meta.ManagedBy)
								continue
							}
							claimWith := func(spec any, user map[string]string, withHash bool) error {
								labels := map[string]string{}
								for k, v := range user {
									labels[k] = v
								}
								if withHash {
									_, hl, err := plan.SpecHash(spec, user)
									if err != nil {
										return err
									}
									labels = hl
								}
								_, etag, err := ad.Get(cmd.Context(), sc, name)
								if err != nil {
									return fmt.Errorf("claim %s: %w", ref, err)
								}
								patch := map[string]any{"metadata": map[string]any{"managed_by": me, "labels": labels}}
								if err := ad.Patch(cmd.Context(), sc, name, patch, etag); err != nil {
									return fmt.Errorf("claim %s on %s: %w", ref, h, err)
								}
								fmt.Fprintf(w, "claimed %s %s\n", h, ref)
								return nil
							}
							if obj := declared[key]; obj != nil {
								// Already in the repository: only take ownership when asked.
								if claim && l.Meta.ManagedBy == "" {
									if err := claimWith(obj.Spec, obj.Labels, len(obj.SecretRefs) == 0); err != nil {
										return err
									}
								}
								continue
							}
							user := userLabels(l.Meta.Labels)
							path := adoptPath(out, h, ki, project, name)
							if _, err := os.Stat(path); err == nil && !overwrite {
								fmt.Fprintf(w, "keep   %s exists\n", rel(out, path))
								continue
							}
							fileSpec := l.Spec
							var extra []string
							if ps, ok := l.Spec.(api.ProjectSpec); ok {
								ps.Hosts = []string{h}
								fileSpec = ps
							} else if ki.ProjectLevel {
								extra = []string{h}
							}
							if err := writeDoc(path, ki.Kind, name, user, fileSpec, extra); err != nil {
								return err
							}
							fmt.Fprintf(w, "wrote  %s\n", rel(out, path))
							if js, ok := l.Spec.(api.JailSpec); ok && len(js.Profiles) > 0 {
								// A repository profile is a fragment sync merges into the jail; the host's own profile objects
								// are not part of the repository, so the file would not validate until they exist as files.
								fmt.Fprintf(w, "note   %s lists the host's profiles %v: the repository does not know them; copy their keys to profiles/<name>.yaml (kind Profile) or into the jail's spec\n", ref, js.Profiles)
							}
							total++
							if claim && l.Meta.ManagedBy == "" {
								hashSpec := l.Spec
								if ps, ok := hashSpec.(api.ProjectSpec); ok {
									ps.Hosts = nil
									hashSpec = ps
								}
								if err := claimWith(hashSpec, user, true); err != nil {
									return err
								}
							}
						}
					}
				}
			}
			fmt.Fprintf(w, "\n%d file(s) written. Review them, move shared ones under shared/, and commit.\n", total)
			if !claim && total > 0 {
				fmt.Fprintln(w, "These objects are not owned by the fleet yet: run adopt --claim (or claim them by hand) before the first sync.")
			}
			return nil
		},
	}
	a.hostFlag(c, &hosts)
	c.Flags().BoolVar(&claim, "claim", false, "mark the adopted objects managed_by this fleet on the host")
	c.Flags().BoolVar(&overwrite, "overwrite", false, "replace existing files")
	c.Flags().StringVar(&out, "out", "", "write under this directory instead of the repository (for review)")
	_ = c.RegisterFlagCompletionFunc("out", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return nil, cobra.ShellCompDirectiveFilterDirs
	})
	return c
}

func rel(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return r
	}
	return p
}

func userLabels(l map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range l {
		if len(k) >= len(config.SyncLabelPrefix) && k[:len(config.SyncLabelPrefix)] == config.SyncLabelPrefix {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// adoptPath is where an adopted object is written: projects and their
// objects under projects/, everything else under hosts/<host>/.
func adoptPath(out, host string, ki model.KindInfo, project, name string) string {
	switch {
	case ki.Kind == api.KindProject:
		return filepath.Join(out, "projects", name+".yaml")
	case ki.ProjectLevel:
		return filepath.Join(out, "projects", project, ki.Dir, name+".yaml")
	}
	return filepath.Join(out, "hosts", host, ki.Dir, name+".yaml")
}

func writeDoc(path string, kind api.Kind, name string, labels map[string]string, spec any, hosts []string) error {
	m, err := model.ToMap(spec)
	if err != nil {
		return err
	}
	meta := map[string]any{"name": name}
	if len(labels) > 0 {
		meta["labels"] = labels
	}
	doc := map[string]any{"apiVersion": api.APIVersion, "kind": string(kind), "metadata": meta, "spec": m}
	if len(hosts) > 0 {
		doc["hosts"] = hosts
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}
