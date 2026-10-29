package dtest

import (
	"context"
	"strings"
	"time"

	"jailops.net/jailops/internal/lifecycle/fakes"
	"jailops.net/jailops/sdk/api"
)

// fileLogs is the log reader of the test daemon: the files below
// <root>/var/log of the in-memory store, with the helper's answers. A jail
// root with no file below it is a dataset that is not mounted.
type fileLogs struct{ f *fakes.Files }

// LogModTime is the modification time every log file of the test daemon reports.
var LogModTime = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func (l fileLogs) mounted(root string) bool {
	for _, p := range l.f.Paths() {
		if strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return false
}

func (l fileLogs) ListLogs(_ context.Context, dir string) ([]api.JailLogFile, bool, error) {
	root, ok := strings.CutSuffix(dir, "/var/log")
	if !ok || !strings.HasPrefix(dir, "/") {
		return nil, false, api.Errorf(api.CodeForbidden, "%q is not the log directory of a jail", dir)
	}
	if !l.mounted(root) {
		return nil, false, api.Errorf(api.CodeNotFound, "jail root %s is missing", root).WithDetail("missing", "root")
	}
	out := []api.JailLogFile{}
	for _, p := range l.f.Paths() {
		rel, ok := strings.CutPrefix(p, dir+"/")
		if !ok || api.ValidLogFileName(rel) != nil {
			continue
		}
		b, _ := l.f.Get(p)
		kind, compressed := api.LogFileKind(rel)
		out = append(out, api.JailLogFile{Name: rel, Size: int64(len(b)), ModifiedAt: LogModTime, Kind: kind, Compressed: compressed})
	}
	return out, false, nil
}

func (l fileLogs) TailFile(_ context.Context, p string, offset, limit int64) ([]byte, int64, int64, error) {
	// the helper's path class: <jail root>/var/log and a plain name at most two segments deep
	root, rel, ok := strings.Cut(p, "/var/log/")
	if !ok || !strings.HasPrefix(p, "/") || api.ValidLogFileName(rel) != nil {
		return nil, 0, 0, api.Errorf(api.CodeForbidden, "path %q is not a log file of a jail", p)
	}
	b, ok := l.f.Get(p)
	if !ok {
		if !l.mounted(root) {
			return nil, 0, 0, api.Errorf(api.CodeNotFound, "jail root %s is missing", root).WithDetail("missing", "root")
		}
		return nil, 0, 0, api.Errorf(api.CodeNotFound, "%s does not exist", p).WithDetail("missing", "file")
	}
	size := int64(len(b))
	start := api.LogWindowStart(size, offset, limit)
	return b[start:], start, size, nil
}
