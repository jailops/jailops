package fake

import (
	"errors"
	"net/http"
	"sort"

	"jailops.net/jailops/sdk/api"
)

// SeedJailLog stores a log file of a jail, name relative to /var/log
// ("messages", "nginx/error.log"). Seeding the same name again replaces the
// content, which is how a test appends to or rotates a file.
func (s *Server) SeedJailLog(project, jail, name string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logs == nil {
		s.logs = map[string]map[string][]byte{}
	}
	key := project + "/" + jail
	if s.logs[key] == nil {
		s.logs[key] = map[string][]byte{}
	}
	s.logs[key][name] = append([]byte(nil), data...)
}

// SeedJailUnmounted makes the log routes of a jail answer as the daemon does
// for a stopped jail whose root is not mounted.
func (s *Server) SeedJailUnmounted(project, jail string, unmounted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unmounted == nil {
		s.unmounted = map[string]bool{}
	}
	s.unmounted[project+"/"+jail] = unmounted
}

// LogReads is the number of log file reads served so far.
func (s *Server) LogReads() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logReads
}

func notMounted(name string) *api.Error {
	return api.Errorf(api.CodeBusy, "jail %q is not mounted", name).
		WithHint("the root of a stopped jail is mounted while it runs: start it, or mount its root dataset on the host")
}

func (s *Server) jailLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	key, j := s.jails.find(r, name)
	files := []api.JailLogFile{}
	unmounted := s.unmounted[key]
	for n, b := range s.logs[key] {
		kind, compressed := api.LogFileKind(n)
		files = append(files, api.JailLogFile{Name: n, Size: int64(len(b)), ModifiedAt: s.now().UTC(), Kind: kind, Compressed: compressed})
	}
	s.mu.Unlock()
	switch {
	case j == nil:
		writeErr(w, notFound("jail", name))
	case unmounted:
		writeErr(w, notMounted(name))
	default:
		sort.Slice(files, func(i, k int) bool { return files[i].Name < files[k].Name })
		writeSync(w, 200, files)
	}
}

func (s *Server) jailLog(w http.ResponseWriter, r *http.Request) {
	name, file := r.PathValue("name"), r.PathValue("file")
	if err := api.ValidLogFileName(file); err != nil {
		writeErr(w, apiError(err))
		return
	}
	q, err := api.ParseLogQuery(r.URL.Query())
	if err != nil {
		writeErr(w, apiError(err))
		return
	}
	s.mu.Lock()
	key, j := s.jails.find(r, name)
	data, ok := s.logs[key][file]
	unmounted := s.unmounted[key]
	now := s.now()
	s.logReads++
	s.mu.Unlock()
	switch {
	case j == nil:
		writeErr(w, notFound("jail", name))
	case unmounted:
		writeErr(w, notMounted(name))
	case !ok:
		writeErr(w, api.Errorf(api.CodeNotFound, "jail %q has no log file %q", name, file))
	default:
		if _, compressed := api.LogFileKind(file); compressed {
			writeErr(w, api.Errorf(api.CodeInvalid, "%s is not readable (compressed)", file).WithHint("read the plain rotated copies, or the file from inside the jail"))
			return
		}
		size := int64(len(data))
		start := api.LogWindowStart(size, q.Offset, api.MaxLogReadBytes)
		writeSync(w, 200, api.AssembleLogRead(name, file, data[start:], start, size, q, now))
	}
}

// apiError returns err as the API error it is.
func apiError(err error) *api.Error {
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	return api.Errorf(api.CodeInternal, "%v", err)
}
