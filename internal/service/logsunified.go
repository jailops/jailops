package service

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	"jailops.net/jailops/internal/logfwd"
	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// Every LogReader serves the forwarder too: it needs fs.tail_file and nothing else.
var _ logfwd.Reader = LogReader(nil)

// logJailRef is a jail whose logs a unified read or the forwarder takes.
type logJailRef struct {
	key, project, name, app string
	labels                  map[string]string
}

// logJails lists the registry's jails that keep returns true for, sorted by key.
func (s *Service) logJails(keep func(project string) bool) []logJailRef {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []logJailRef
	for _, k := range sortedKeys(s.jails) {
		m := s.jails[k].jail.Metadata
		if keep(m.Project) {
			out = append(out, logJailRef{key: k, project: m.Project, name: m.Name, app: m.Labels[api.LabelApp], labels: m.Labels})
		}
	}
	return out
}

// appLogFiles are the files below /var/log that the app of a jail declares,
// as names relative to /var/log. A declared directory stands for the active
// files in it, which needs the jail's root mounted; paths elsewhere (below
// /var/db or /usr/local) are not readable through the helper and left out.
func (s *Service) appLogFiles(ctx context.Context, key, app string) []string {
	if app == "" {
		return nil
	}
	e, ok, err := s.findApp(app)
	if err != nil || !ok || e.Err != nil {
		return nil
	}
	var listing []api.JailLogFile
	listed := false
	var out []string
	for _, l := range e.App.Logs {
		rel, ok := strings.CutPrefix(l.Path, "/var/log/")
		if !ok || api.ValidLogFileName(rel) != nil {
			continue
		}
		if !listed {
			listed = true
			listing, _, _ = s.d.Logs.ListLogs(ctx, JailLogDir(s.rootMount(key)))
		}
		dir := false
		for _, f := range listing {
			if strings.HasPrefix(f.Name, rel+"/") && f.Kind == api.JailLogActive {
				dir = true
				out = append(out, f.Name)
			}
		}
		if !dir {
			out = append(out, rel)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// ReadLogs reads the end of one file (default messages, plus the logs of
// installed apps with q.AppLogs) of every selected jail of the request's
// project, or of every project the caller may use when the request names
// none, and merges the lines by time. At most api.MaxLogSources files are
// read, each at most api.MaxLogReadBytes from its end (or from the offset
// q.Cursor holds for it); at most api.MaxLogMergedLines lines are returned,
// the newest. A source that cannot be read (a jail that is not mounted)
// carries its error and the rest are read. Audited like a jail log read.
func (s *Service) ReadLogs(ctx context.Context, q api.LogsQuery) (api.LogsRead, error) {
	q, err := q.Normalize()
	if err != nil {
		return api.LogsRead{}, err
	}
	if s.d.Logs == nil {
		return api.LogsRead{}, api.Errorf(api.CodeUnsupported, "this daemon has no way to read jail logs").
			WithHint("jailops-priv offers log reading; run jailopsd with the helper")
	}
	all, projects, err := s.visibleProjects(ctx)
	if err != nil {
		return api.LogsRead{}, err
	}
	jails := s.logJails(func(p string) bool { return all || slices.Contains(projects, p) })
	out := api.LogsRead{Lines: []api.LogEntry{}, Sources: []api.LogSource{}, Next: map[string]int64{}}
	type src struct {
		j    logJailRef
		file string
	}
	var srcs []src
	for _, j := range jails {
		if (len(q.Jails) > 0 && !slices.Contains(q.Jails, j.name)) || !MatchLabels(j.labels, q.Labels) || (q.App != "" && j.app != q.App) {
			continue
		}
		files := []string{q.File}
		if q.AppLogs {
			files = append(files, s.appLogFiles(ctx, j.key, j.app)...)
		}
		seen := map[string]bool{}
		for _, f := range files {
			if !seen[f] {
				seen[f] = true
				srcs = append(srcs, src{j, f})
			}
		}
	}
	if len(srcs) > api.MaxLogSources {
		srcs, out.Truncated = srcs[:api.MaxLogSources], true
	}
	now := s.now()
	var merged []api.LogSourceLines
	for _, sr := range srcs {
		key := api.LogSourceKey(sr.j.project, sr.j.name, sr.file)
		ls := api.LogSource{Key: key, Project: sr.j.project, Jail: sr.j.name, App: sr.j.app, File: sr.file}
		offset := q.Cursor[key]
		data, start, size, err := s.d.Logs.TailFile(ctx, path.Join(JailLogDir(s.rootMount(sr.j.key)), sr.file), offset, api.MaxLogReadBytes)
		if err != nil {
			j, _ := s.getByKey(ctx, sr.j.key)
			var ae *api.Error
			if !asAPI(s.logErr(err, sr.j.name, sr.j.key, sr.file, j), &ae) {
				return api.LogsRead{}, internal(err)
			}
			ls.Error, ls.NextOffset = ae, offset
			out.Next[key] = offset
			out.Sources = append(out.Sources, ls)
			continue
		}
		r := api.AssembleLogRead(sr.j.name, sr.file, data, start, size, api.JailLogQuery{Tail: q.Tail, Since: q.Since, Grep: q.Grep, Offset: offset}, now)
		ls.NextOffset, ls.Rotated, ls.Truncated = r.NextOffset, r.Rotated, r.Truncated
		out.Next[key] = r.NextOffset
		out.Sources = append(out.Sources, ls)
		lines := make([]string, len(r.Lines))
		for i, l := range r.Lines {
			lines[i] = l.Text
		}
		merged = append(merged, api.LogSourceLines{Project: sr.j.project, Jail: sr.j.name, App: sr.j.app, File: sr.file, Lines: lines})
	}
	var cut bool
	out.Lines, cut = api.MergeLogs(merged, api.MaxLogMergedLines, now)
	out.Truncated = out.Truncated || cut
	s.auditUnified(ctx, q, len(srcs))
	return out, nil
}

// auditUnified records a unified read once per identity and selection
// within logAuditWindow: a follower asks every two seconds.
func (s *Service) auditUnified(ctx context.Context, q api.LogsQuery, sources int) {
	a := ActorFrom(ctx)
	sel := fmt.Sprintf("project=%q jails=%v labels=%v app=%q file=%s app_logs=%v", ProjectFrom(ctx), q.Jails, q.Labels, q.App, q.File, q.AppLogs)
	id := a.Kind + ":" + a.Name + "|unified|" + sel
	if !s.logAud.due(id, s.now()) {
		return
	}
	s.Audit(ctx, store.AuditEntry{Actor: a.Name, ActorKind: a.Kind, Method: "LOGS", Route: "/api/v1/logs", Object: "/api/v1/logs",
		Project: ProjectFrom(ctx), Result: "ok", Remote: a.Remote, Detail: fmt.Sprintf("read %s sources=%d tail=%d since=%q grep=%q", sel, sources, q.Tail, q.Since, q.Grep)})
}

// due reports whether id was not recorded within logAuditWindow, and records it.
func (l *logAudit) due(id string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[string]time.Time{}
	}
	if t, ok := l.seen[id]; ok && now.Sub(t) < logAuditWindow {
		return false
	}
	if len(l.seen) >= 4096 {
		for k, t := range l.seen {
			if now.Sub(t) >= logAuditWindow {
				delete(l.seen, k)
			}
		}
	}
	l.seen[id] = now
	return true
}
