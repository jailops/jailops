package service

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sync"
	"time"

	"jailops.net/jailops/internal/store"
	"jailops.net/jailops/sdk/api"
)

// LogReader reads the log files below a jail root. In production it is the
// privileged helper, which accepts only the log directory of a jail root and
// plain names below it; tests use an in-memory one.
type LogReader interface {
	// ListLogs lists the regular files of dir (<jail root>/var/log) and of
	// its direct subdirectories; truncated says that a limit of the reader
	// left entries out. A missing or empty jail root is not_found with the
	// detail missing=root.
	ListLogs(ctx context.Context, dir string) (files []api.JailLogFile, truncated bool, err error)
	// TailFile reads path from max(offset, size-limit) to its end; a file
	// shorter than offset is read from the beginning. It returns the bytes,
	// where they start and the size of the file.
	TailFile(ctx context.Context, path string, offset, limit int64) (data []byte, start, size int64, err error)
}

// JailLogDir is the log directory of a jail root.
func JailLogDir(root string) string { return path.Join(root, "var", "log") }

// logAuditWindow is how long one identity's reads of one log file of one
// jail are covered by a single audit entry: a client following a file polls
// every second, and an entry per poll would drown the log.
const logAuditWindow = 5 * time.Minute

// logAudit remembers the last audited read per identity, jail and file.
type logAudit struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// JailLogs lists the log files of a jail of the request's project. A jail
// whose root is not mounted (stopped) answers busy with a hint. The listing
// is audited like a read. truncated says that the reader's limits left
// files out.
func (s *Service) JailLogs(ctx context.Context, name string) ([]api.JailLogFile, bool, error) {
	p, key, j, err := s.logJail(ctx, name)
	if err != nil {
		return nil, false, err
	}
	files, truncated, err := s.d.Logs.ListLogs(ctx, JailLogDir(s.rootMount(key)))
	if err != nil {
		return nil, false, s.logErr(err, name, key, "", j)
	}
	if files == nil {
		files = []api.JailLogFile{}
	}
	s.auditLog(ctx, p.Meta.Name, name, key, "", "list")
	return files, truncated, nil
}

// JailLog reads the end of one log file of a jail (file is relative to
// /var/log), at most api.MaxLogReadBytes from the end, and applies the
// query: since, grep, tail. Rotated copies that are compressed are refused.
func (s *Service) JailLog(ctx context.Context, name, file string, q api.JailLogQuery) (api.JailLogRead, error) {
	if err := api.ValidLogFileName(file); err != nil {
		return api.JailLogRead{}, err
	}
	q, err := q.Normalize()
	if err != nil {
		return api.JailLogRead{}, err
	}
	p, key, j, err := s.logJail(ctx, name)
	if err != nil {
		return api.JailLogRead{}, err
	}
	if _, compressed := api.LogFileKind(file); compressed {
		return api.JailLogRead{}, api.Errorf(api.CodeInvalid, "%s is not readable (compressed)", file).
			WithHint("read the plain rotated copies, or the file from inside the jail")
	}
	data, start, size, err := s.d.Logs.TailFile(ctx, path.Join(JailLogDir(s.rootMount(key)), file), q.Offset, api.MaxLogReadBytes)
	if err != nil {
		return api.JailLogRead{}, s.logErr(err, name, key, file, j)
	}
	s.auditLog(ctx, p.Meta.Name, name, key, file, fmt.Sprintf("read %s tail=%d since=%q grep=%q offset=%d", file, q.Tail, q.Since, q.Grep, q.Offset))
	return api.AssembleLogRead(name, file, data, start, size, q, s.now()), nil
}

// logJail resolves the jail of a log request and checks that this daemon can read logs.
func (s *Service) logJail(ctx context.Context, name string) (project, string, api.Jail, error) {
	if s.d.Logs == nil {
		return project{}, "", api.Jail{}, api.Errorf(api.CodeUnsupported, "this daemon has no way to read jail logs").
			WithHint("jailops-priv offers log reading; run jailopsd with the helper")
	}
	p, key, err := s.jailRef(ctx, name)
	if err != nil {
		return p, "", api.Jail{}, err
	}
	j, err := s.getByKey(ctx, key)
	return p, key, j, err
}

// logErr maps what the reader answered: a missing or empty jail root is a
// jail that is not mounted (its root is mounted only while it runs, or when
// mounted by hand); a missing file is not_found in the jail's terms.
func (s *Service) logErr(err error, name, key, file string, j api.Jail) error {
	var ae *api.Error
	if !errors.As(err, &ae) || ae.Code != api.CodeNotFound {
		return internal(err)
	}
	if ae.Detail["missing"] == "root" {
		hint := "start the jail, or mount its root dataset on the host: zfs mount " + s.lcfg.RootDS(key)
		if j.Status.Power == api.PowerRunning {
			hint = "the jail runs but its root is empty; check the dataset " + s.lcfg.RootDS(key)
		}
		return api.Errorf(api.CodeBusy, "jail %q is not mounted", name).WithHint(hint)
	}
	return api.Errorf(api.CodeNotFound, "jail %q has no log file %q", name, file).WithHint("list them with GET /api/v1/jails/" + name + "/logs")
}

// auditLog records a log read once per identity, jail and file within
// logAuditWindow.
func (s *Service) auditLog(ctx context.Context, project, name, key, file, detail string) {
	a := ActorFrom(ctx)
	id := a.Kind + ":" + a.Name + "|" + key + "|" + file
	now := s.now()
	s.logAud.mu.Lock()
	if s.logAud.seen == nil {
		s.logAud.seen = map[string]time.Time{}
	}
	if t, ok := s.logAud.seen[id]; ok && now.Sub(t) < logAuditWindow {
		s.logAud.mu.Unlock()
		return
	}
	if len(s.logAud.seen) >= 4096 {
		for k, t := range s.logAud.seen {
			if now.Sub(t) >= logAuditWindow {
				delete(s.logAud.seen, k)
			}
		}
	}
	s.logAud.seen[id] = now
	s.logAud.mu.Unlock()
	route := "/api/v1/jails/{name}/logs"
	if file != "" {
		route += "/{file...}"
	}
	s.Audit(ctx, store.AuditEntry{Actor: a.Name, ActorKind: a.Kind, Method: "LOGS", Route: route, Object: "/api/v1/jails/" + name,
		Project: project, Result: "ok", Remote: a.Remote, Detail: detail})
}
