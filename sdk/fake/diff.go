package fake

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// jailDiff answers GET /jails/{name}/diff with the same two changes for every FreeBSD jail: a line added to rc.conf and
// a configuration file in /usr/local/etc.
func (s *Server) jailDiff(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	_, j := s.jails.find(r, r.PathValue("name"))
	var linux bool
	if j != nil {
		linux = j.Spec.OS == api.OSLinux
	}
	s.mu.Unlock()
	if j == nil {
		writeErr(w, notFound("jail", r.PathValue("name")))
		return
	}
	if linux {
		writeErr(w, api.Errorf(api.CodeInvalid, "jail %q is a Linux jail: it has a full copy of its image, so there is nothing to compare it with", j.Metadata.Name))
		return
	}
	q := r.URL.Query()
	limit := api.DefaultDiffLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	mk := func(name, path string, es ...api.DiffEntry) api.DiffDataset {
		d := api.DiffDataset{Name: name, Path: path, Base: api.DiffBaseBaseline, Entries: []api.DiffEntry{}, Counts: map[string]int{}}
		for _, e := range es {
			d.Total++
			d.Counts[e.Kind]++
			if len(d.Entries) < limit {
				d.Entries = append(d.Entries, e)
			}
		}
		d.Truncated = d.Total > len(d.Entries)
		return d
	}
	image, _, _ := strings.Cut(j.Spec.Image, "@")
	root := mk("root", "/")
	root.Base = api.DiffBaseImage
	all := []api.DiffDataset{root,
		mk("etc", "/etc", api.DiffEntry{Kind: api.DiffModified, Path: "/etc/rc.conf"}),
		mk("local", "/usr/local", api.DiffEntry{Kind: api.DiffAdded, Path: "/usr/local/etc/web.conf"}),
		mk("var-db", "/var/db"), mk("var-log", "/var/log"), mk("var-mail", "/var/mail"), mk("var-spool", "/var/spool"),
		mk("home", "/home"), mk("rootdir", "/root")}
	if ds := q.Get("dataset"); ds != "" && !slices.ContainsFunc(all, func(d api.DiffDataset) bool { return d.Name == ds }) {
		writeErr(w, api.Errorf(api.CodeInvalid, "jail %q has no dataset %q to compare: root, etc, local, var-db, var-log, var-mail, var-spool, home, rootdir", j.Metadata.Name, ds))
		return
	}
	out := api.JailDiff{Jail: j.Metadata.Name, Image: image + "#g1", Datasets: []api.DiffDataset{}}
	for _, d := range all {
		if ds := q.Get("dataset"); ds == "" || ds == d.Name {
			out.Datasets = append(out.Datasets, d)
		}
	}
	if q.Get("packages") == "true" {
		out.Packages = &api.DiffPackages{Packages: []string{}}
	}
	writeSync(w, 200, out)
}
