package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// The unified log view and log forwarding of the fake. The view reads the
// files of SeedJailLog like the daemon reads jail roots; forwarding keeps
// the sink list and reports what SeedLogSinkStatus says (nothing is sent).

type fakeForward struct {
	file     []api.LogSink
	stored   *api.LogForwardSpec
	gen      int64
	status   map[string]api.LogSinkStatus
	testFail map[string]string
	tests    int
}

// SeedLogForward sets the sink list of jailopsd.yaml (logging.forward).
func (s *Server) SeedLogForward(sinks []api.LogSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fwd.file = slices.Clone(sinks)
}

// SeedLogSinkStatus sets what the status of a sink reports (name, type and to are filled in).
func (s *Server) SeedLogSinkStatus(st api.LogSinkStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fwd.status == nil {
		s.fwd.status = map[string]api.LogSinkStatus{}
	}
	s.fwd.status[st.Name] = st
}

// SeedLogSinkTestFailure makes a test of the sink fail with msg ("" succeeds again).
func (s *Server) SeedLogSinkTestFailure(sink, msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fwd.testFail == nil {
		s.fwd.testFail = map[string]string{}
	}
	s.fwd.testFail[sink] = msg
}

// handleLogs merges the files of the selected jails like GET /api/v1/logs of the daemon.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q, err := api.ParseLogsQuery(r.URL.Query())
	if err != nil {
		writeErr(w, apiError(err))
		return
	}
	rc := s.info(r)
	project := rc.project
	if v := r.URL.Query().Get("all_projects"); v == "true" || v == "1" {
		project = ""
	}
	s.mu.Lock()
	type src struct {
		key, project, jail, app, file string
	}
	var srcs []src
	keys := make([]string, 0, len(s.jails.items))
	for k := range s.jails.items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		j := s.jails.items[k]
		p, name := j.Metadata.Project, j.Metadata.Name
		if (project != "" && p != project) || !rc.canProject(p) {
			continue
		}
		app := j.Metadata.Labels[api.LabelApp]
		if (len(q.Jails) > 0 && !slices.Contains(q.Jails, name)) || (q.App != "" && app != q.App) || !matchLabels(j.Metadata.Labels, q.Labels) {
			continue
		}
		files := []string{q.File}
		if q.AppLogs && app != "" && s.apps[app] != nil {
			for _, l := range s.apps[app].detail.Definition.Logs {
				if rel, ok := strings.CutPrefix(l.Path, "/var/log/"); ok && api.ValidLogFileName(rel) == nil && !slices.Contains(files, rel) {
					files = append(files, rel)
				}
			}
		}
		for _, f := range files {
			srcs = append(srcs, src{p + "/" + name, p, name, app, f})
		}
	}
	out := api.LogsRead{Lines: []api.LogEntry{}, Sources: []api.LogSource{}, Next: map[string]int64{}}
	if len(srcs) > api.MaxLogSources {
		srcs, out.Truncated = srcs[:api.MaxLogSources], true
	}
	now := s.now()
	var merged []api.LogSourceLines
	for _, sr := range srcs {
		key := api.LogSourceKey(sr.project, sr.jail, sr.file)
		ls := api.LogSource{Key: key, Project: sr.project, Jail: sr.jail, App: sr.app, File: sr.file}
		offset := q.Cursor[key]
		data, ok := s.logs[sr.key][sr.file]
		switch {
		case s.unmounted[sr.key]:
			ls.Error = notMounted(sr.jail)
		case !ok:
			ls.Error = api.Errorf(api.CodeNotFound, "jail %q has no log file %q", sr.jail, sr.file)
		}
		if ls.Error != nil {
			ls.NextOffset, out.Next[key] = offset, offset
			out.Sources = append(out.Sources, ls)
			continue
		}
		size := int64(len(data))
		start := api.LogWindowStart(size, offset, api.MaxLogReadBytes)
		rd := api.AssembleLogRead(sr.jail, sr.file, data[start:], start, size, api.JailLogQuery{Tail: q.Tail, Since: q.Since, Grep: q.Grep, Offset: offset}, now)
		ls.NextOffset, ls.Rotated, ls.Truncated = rd.NextOffset, rd.Rotated, rd.Truncated
		out.Next[key] = rd.NextOffset
		out.Sources = append(out.Sources, ls)
		lines := make([]string, len(rd.Lines))
		for i, l := range rd.Lines {
			lines[i] = l.Text
		}
		merged = append(merged, api.LogSourceLines{Project: sr.project, Jail: sr.jail, App: sr.app, File: sr.file, Lines: lines})
	}
	s.logReads++
	s.mu.Unlock()
	var cut bool
	out.Lines, cut = api.MergeLogs(merged, api.MaxLogMergedLines, now)
	out.Truncated = out.Truncated || cut
	writeSync(w, 200, out)
}

func matchLabels(labels, want map[string]string) bool {
	for k, v := range want {
		if l, ok := labels[k]; !ok || l != v {
			return false
		}
	}
	return true
}

// forwardView is the LogForward reply; call with s.mu held.
func (s *Server) forwardView() api.LogForward {
	sinks, origin := s.fwd.file, api.LogForwardFromConfig
	if s.fwd.stored != nil {
		sinks, origin = s.fwd.stored.Sinks, api.LogForwardFromAPI
	}
	out := api.LogForward{Kind: api.KindLogForward, Spec: api.LogForwardSpec{Sinks: slices.Clone(sinks)},
		Status: api.LogForwardStatus{Origin: origin, Generation: s.fwd.gen, Sinks: []api.LogSinkStatus{}}}
	if out.Spec.Sinks == nil {
		out.Spec.Sinks = []api.LogSink{}
	}
	if s.fwd.stored != nil {
		t := s.now().UTC()
		out.Status.UpdatedAt = &t
	}
	for _, k := range sinks {
		st := s.fwd.status[k.Name]
		st.Name, st.Type, st.To = k.Name, k.Type, k.To
		if k.Plaintext() && st.Warning == "" {
			tr, hp, _ := k.SinkTarget()
			st.Warning = fmt.Sprintf("plain %s to %s sends the logs in the clear; use tls://", tr, hp)
		}
		out.Status.Sinks = append(out.Status.Sinks, st)
	}
	return out
}

func (s *Server) writeForward(w http.ResponseWriter) {
	v := s.forwardView()
	if v.Status.Generation > 0 {
		w.Header().Set("ETag", etagOf(v.Status.Generation))
	}
	writeSync(w, 200, v)
}

func (s *Server) handleLogForward(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeForward(w)
}

func (s *Server) handleLogForwardPut(w http.ResponseWriter, r *http.Request) {
	var spec api.LogForwardSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, api.Errorf(api.CodeInvalid, "invalid JSON body: %v", err))
		return
	}
	if spec.Sinks == nil {
		spec.Sinks = []api.LogSink{}
	}
	if err := spec.Validate(); err != nil {
		writeErr(w, apiError(err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h := strings.Trim(strings.TrimPrefix(r.Header.Get("If-Match"), "W/"), `"`); h != "" && h != "*" {
		if n, err := strconv.ParseInt(h, 10, 64); err != nil || n != s.fwd.gen {
			writeErr(w, api.Errorf(api.CodeConflict, "the log forwarding changed: current generation is %d", s.fwd.gen).WithHint("read it again and re-apply your change"))
			return
		}
	}
	s.fwd.stored = &spec
	s.fwd.gen++
	s.writeForward(w)
}

func (s *Server) handleLogForwardDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fwd.stored, s.fwd.gen = nil, 0
	s.writeForward(w)
}

func (s *Server) handleLogForwardTest(w http.ResponseWriter, r *http.Request) {
	var t api.LogForwardTest
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, api.Errorf(api.CodeInvalid, "invalid JSON body: %v", err))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range s.forwardView().Spec.Sinks {
		if k.Name != t.Sink {
			continue
		}
		s.fwd.tests++
		res := api.LogForwardTestResult{Sink: k.Name, OK: true, Millis: 3,
			Line: fmt.Sprintf("jailops log forwarding test %06d from %s", s.fwd.tests, s.hostname)}
		if msg := s.fwd.testFail[k.Name]; msg != "" {
			res.OK, res.Error = false, msg
		}
		writeSync(w, 200, res)
		return
	}
	writeErr(w, api.Errorf(api.CodeNotFound, "sink %q not found", t.Sink).WithHint("list them with: jailops logs forward list"))
}
