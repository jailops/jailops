package fake

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The files of a jail (extension jail_files), kept in memory: the same routes as the daemon, with the rules of the
// daemon that a client can see (plain segments only, symlinks stored as text and never followed, no set-id bits).

type fnode struct {
	kind byte // 'f' file, 'd' directory, 'l' symlink
	data []byte
	mode uint32
	link string
	mt   time.Time
}

type fakeFiles struct {
	trees map[string]map[string]*fnode // jail key -> path -> node ("" is the root)
}

func (f *fakeFiles) tree(key string) map[string]*fnode {
	if f.trees == nil {
		f.trees = map[string]map[string]*fnode{}
	}
	t := f.trees[key]
	if t == nil {
		t = map[string]*fnode{"": {kind: 'd', mode: 0o755}}
		f.trees[key] = t
	}
	return t
}

// SeedJailFile puts a regular file into a jail of a project (its directories are made), for a test.
func (s *Server) SeedJailFile(project, jail, p string, data []byte, mode uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.files.tree(project + "/" + jail)
	p = strings.TrimPrefix(p, "/")
	mkParents(t, p, s.now())
	t[p] = &fnode{kind: 'f', data: append([]byte(nil), data...), mode: mode, mt: s.now()}
}

// JailFile reads a regular file of a jail of a project back, for a test.
func (s *Server) JailFile(project, jail, p string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.files.tree(project + "/" + jail)[strings.TrimPrefix(p, "/")]
	if n == nil || n.kind != 'f' {
		return nil, false
	}
	return append([]byte(nil), n.data...), true
}

func mkParents(t map[string]*fnode, p string, now time.Time) {
	for d := path.Dir(p); d != "." && d != ""; d = path.Dir(d) {
		if t[d] == nil {
			t[d] = &fnode{kind: 'd', mode: 0o755, mt: now}
		}
	}
}

func (s *Server) fileJailOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	s.mu.Lock()
	key, j := s.jails.find(r, name)
	unmounted := s.unmounted[key]
	s.mu.Unlock()
	switch {
	case j == nil:
		writeErr(w, notFound("jail", name))
	case unmounted:
		writeErr(w, notMounted(name))
	default:
		return key, true
	}
	return "", false
}

// under finds the children of directory d.
func children(t map[string]*fnode, d string) []string {
	var out []string
	for p := range t {
		if p == "" || path.Dir(p) != dirKey(d) {
			continue
		}
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func dirKey(d string) string {
	if d == "" {
		return "."
	}
	return d
}

func (s *Server) jailFileGet(w http.ResponseWriter, r *http.Request) {
	key, ok := s.fileJailOf(w, r)
	if !ok {
		return
	}
	p, err := api.CleanJailFilePath(r.PathValue("path"))
	if err != nil {
		writeErr(w, apiError(err))
		return
	}
	q := r.URL.Query()
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.files.tree(key)
	n := t[p]
	if q.Get(api.FileQueryStat) == "true" {
		st := api.JailFileStat{Path: "/" + p, Type: api.FileTypeNone}
		if n != nil {
			mt := n.mt.UTC()
			st.ModifiedAt = &mt
			st.Mode = fmt.Sprintf("%04o", n.mode&0o777)
			switch n.kind {
			case 'f':
				st.Type, st.Size = api.FileTypeFile, int64(len(n.data))
			case 'd':
				st.Type = api.FileTypeDir
			case 'l':
				st.Type, st.Link = api.FileTypeSymlink, n.link
			}
		}
		writeSync(w, 200, st)
		return
	}
	if q.Get(api.FileQueryList) == "true" {
		if n == nil {
			writeErr(w, notFound("file", "/"+p))
			return
		}
		if n.kind != 'd' {
			writeErr(w, api.Errorf(api.CodeInvalid, "/%s is not a directory", p))
			return
		}
		l := api.JailFileList{Path: "/" + p, Entries: []api.JailFileEntry{}}
		for _, c := range children(t, p) {
			if len(l.Entries) == api.MaxFileListEntries {
				l.Truncated = true
				break
			}
			e := api.JailFileEntry{Name: path.Base(c), Type: api.FileTypeFile}
			switch t[c].kind {
			case 'd':
				e.Type = api.FileTypeDir
			case 'l':
				e.Type = api.FileTypeSymlink
			}
			l.Entries = append(l.Entries, e)
		}
		writeSync(w, 200, l)
		return
	}
	if n == nil {
		writeErr(w, notFound("file", "/"+p))
		return
	}
	if q.Get(api.FileQueryArchive) == api.FileArchiveTar {
		if p == "" {
			writeErr(w, api.Errorf(api.CodeInvalid, "name a file or a directory of the jail, not its root"))
			return
		}
		if n.kind == 'd' && q.Get(api.FileQueryRecursive) != "true" {
			writeErr(w, api.Errorf(api.CodeForbidden, "%s is a directory (copy it with -r)", path.Base(p)))
			return
		}
		as := q.Get(api.FileQueryAs)
		if as == "" {
			as = path.Base(p)
		}
		w.Header().Set("Content-Type", api.MediaTar)
		w.WriteHeader(200)
		tw := tar.NewWriter(w)
		var walk func(src, dst string)
		walk = func(src, dst string) {
			nn := t[src]
			h := &tar.Header{Name: dst, Mode: int64(nn.mode & 0o777), ModTime: nn.mt}
			switch nn.kind {
			case 'f':
				h.Typeflag, h.Size = tar.TypeReg, int64(len(nn.data))
				_ = tw.WriteHeader(h)
				_, _ = tw.Write(nn.data)
			case 'l':
				h.Typeflag, h.Linkname = tar.TypeSymlink, nn.link
				_ = tw.WriteHeader(h)
			case 'd':
				h.Typeflag, h.Name = tar.TypeDir, dst+"/"
				_ = tw.WriteHeader(h)
				for _, c := range children(t, src) {
					walk(c, dst+"/"+path.Base(c))
				}
			}
		}
		walk(p, as)
		_ = tw.Close()
		return
	}
	if n.kind != 'f' {
		writeErr(w, api.Errorf(api.CodeInvalid, "/%s is not a regular file", p))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(n.data)))
	w.Header().Set(api.HeaderFileMode, fmt.Sprintf("%04o", n.mode&0o777))
	_, _ = w.Write(n.data)
}

func (s *Server) jailFilePut(w http.ResponseWriter, r *http.Request) {
	key, ok := s.fileJailOf(w, r)
	if !ok {
		return
	}
	p, err := api.CleanJailFilePath(r.PathValue("path"))
	if err != nil {
		writeErr(w, apiError(err))
		return
	}
	q := r.URL.Query()
	mkdir := q.Get(api.FileQueryMkdir) == "true"
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.files.tree(key)
	var res api.JailFileCopy
	put := func(dir string, untar bool) *api.Error {
		for d := dir; d != ""; d = path.Dir(d) {
			if d == "." {
				break
			}
			n := t[d]
			switch {
			case n == nil && mkdir:
				mkParents(t, d+"/x", s.now())
				t[d] = &fnode{kind: 'd', mode: 0o755, mt: s.now()}
			case n == nil:
				return api.Errorf(api.CodeNotFound, "the directory /%s does not exist in the jail", dir).WithDetail("missing", "file")
			case n.kind == 'l':
				return api.Errorf(api.CodeForbidden, "%s is a symlink: a symlink is never followed", path.Base(d))
			case n.kind != 'd':
				return api.Errorf(api.CodeInvalid, "%s is not a directory", path.Base(d))
			}
		}
		if !untar {
			return nil
		}
		tr := tar.NewReader(r.Body)
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				return nil
			}
			if err != nil {
				return api.Errorf(api.CodeForbidden, "the archive is damaged: %v", err)
			}
			name := strings.TrimSuffix(h.Name, "/")
			if c, cerr := api.CleanJailFilePath(name); cerr != nil || c != name || strings.HasPrefix(name, "/") {
				return api.Errorf(api.CodeForbidden, "the name %q is not a relative, clean name", h.Name)
			}
			full := path.Join(dir, name)
			mkParents(t, full, s.now())
			if old := t[full]; old != nil && old.kind == 'd' && h.Typeflag != tar.TypeDir {
				return api.Errorf(api.CodeForbidden, "%s is a directory: a directory is never replaced by a file", name)
			}
			mode := uint32(h.Mode) & 0o777
			switch h.Typeflag {
			case tar.TypeDir:
				if t[full] == nil {
					t[full] = &fnode{kind: 'd', mode: mode, mt: s.now()}
				}
				res.Dirs++
			case tar.TypeReg:
				b, _ := io.ReadAll(tr)
				t[full] = &fnode{kind: 'f', data: b, mode: mode, mt: s.now()}
				res.Files++
				res.Bytes += int64(len(b))
			case tar.TypeSymlink:
				t[full] = &fnode{kind: 'l', link: h.Linkname, mt: s.now()}
				res.Symlinks++
			default:
				return api.Errorf(api.CodeForbidden, "%s is a device, FIFO or other special file: only files, directories and symlinks are copied", name)
			}
		}
	}
	if q.Get(api.FileQueryArchive) == api.FileArchiveTar {
		if e := put(p, true); e != nil {
			writeErr(w, e)
			return
		}
		writeSync(w, 200, res)
		return
	}
	if p == "" {
		writeErr(w, api.Errorf(api.CodeInvalid, "name the file to write"))
		return
	}
	mode := uint32(0o644)
	if m := q.Get(api.FileQueryMode); m != "" {
		v, err := strconv.ParseUint(m, 8, 32)
		if err != nil || v&^0o777 != 0 {
			writeErr(w, api.Errorf(api.CodeInvalid, "mode %q must be octal permission bits (0644)", m))
			return
		}
		mode = uint32(v)
	}
	if r.ContentLength < 0 {
		writeErr(w, api.Errorf(api.CodeInvalid, "a plain upload needs a Content-Length"))
		return
	}
	dir := path.Dir(p)
	if dir == "." {
		dir = ""
	}
	if e := put(dir, false); e != nil {
		writeErr(w, e)
		return
	}
	if old := t[p]; old != nil && old.kind == 'd' {
		writeErr(w, api.Errorf(api.CodeForbidden, "%s is a directory: a directory is never replaced by a file", path.Base(p)))
		return
	}
	b, _ := io.ReadAll(r.Body)
	t[p] = &fnode{kind: 'f', data: b, mode: mode, mt: s.now()}
	writeSync(w, 200, api.JailFileCopy{Files: 1, Bytes: int64(len(b))})
}
