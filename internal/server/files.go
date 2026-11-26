package server

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/internal/service"
	"jailops.net/jailops/sdk/api"
)

// The file routes: GET and PUT /api/v1/jails/<n>/files/<path> (sdk/api/jailfiles.go). The body is streamed, never
// held: a transfer is bounded by the byte limit of the daemon (limits.file_copy_bytes), not by max_body_bytes, and its
// read and write deadlines are lifted for the length of the transfer. Both are operator routes, like exec.

func (s *Server) lift(c *rc) {
	rcn := http.NewResponseController(c.w)
	_ = rcn.SetReadDeadline(time.Time{})
	_ = rcn.SetWriteDeadline(time.Time{})
}

// lazyWriter commits the status and the headers of a download with its first byte: an error before it is an ordinary
// error reply, an error after it can only be told in a trailer.
type lazyWriter struct {
	c       *rc
	header  func(h http.Header)
	started bool
	n       int64
}

func (w *lazyWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if !w.started {
		w.started = true
		if w.header != nil {
			w.header(w.c.w.Header())
		}
		w.c.status = http.StatusOK
		w.c.w.WriteHeader(http.StatusOK)
	}
	n, err := w.c.w.Write(p)
	w.n += int64(n)
	return n, err
}

func (s *Server) handleFileGet(c *rc) error {
	name, p := c.pathArg("name"), c.pathArg("path")
	c.object = "/api/v1/jails/" + name
	q := c.r.URL.Query()
	ctx := c.r.Context()
	s.lift(c)
	if q.Get(api.FileQueryStat) == "true" {
		st, err := s.svc.JailFileStat(ctx, name, p)
		if err != nil {
			return err
		}
		c.sync(200, st, "")
		return nil
	}
	if q.Get(api.FileQueryList) == "true" {
		l, err := s.svc.JailFileList(ctx, name, p)
		if err != nil {
			return err
		}
		c.sync(200, l, "")
		return nil
	}
	flag := func(k string) bool { return q.Get(k) == "true" }
	opts := service.JailFileGetOpts{Recursive: flag(api.FileQueryRecursive), Preserve: flag(api.FileQueryPreserve), As: q.Get(api.FileQueryAs)}
	if opts.As != "" && strings.ContainsAny(opts.As, "/\x00") {
		return api.Errorf(api.CodeInvalid, "as must be one name")
	}
	if a := q.Get(api.FileQueryArchive); a != "" && a != api.FileArchiveTar {
		return api.Errorf(api.CodeInvalid, "archive must be %q", api.FileArchiveTar)
	}
	if q.Get(api.FileQueryArchive) == api.FileArchiveTar {
		return s.fileTar(c, name, p, opts)
	}
	return s.fileRaw(c, name, p)
}

// fileTar streams the tar of a path.
func (s *Server) fileTar(c *rc, name, p string, opts service.JailFileGetOpts) error {
	w := &lazyWriter{c: c, header: func(h http.Header) {
		h.Set("Content-Type", api.MediaTar)
		h.Set("Trailer", api.HeaderSkipped+", "+api.HeaderFileError)
	}}
	ctx := c.r.Context()
	project, st, err := s.svc.JailFileGet(ctx, name, p, opts, w)
	if err != nil {
		if !w.started {
			return err
		}
		var ae *api.Error
		msg := err.Error()
		if errors.As(err, &ae) {
			msg = ae.Message
		}
		c.w.Header().Set(api.HeaderFileError, strings.NewReplacer("\n", " ", "\r", " ").Replace(msg))
		c.detail = "file get /" + p + " failed after " + strconv.FormatInt(w.n, 10) + " bytes"
		return nil
	}
	c.w.Header().Set(api.HeaderSkipped, strconv.Itoa(st.Skipped))
	c.detail = fmt.Sprintf("file get /%s files=%d dirs=%d bytes=%d", p, st.Files, st.Dirs, st.Bytes)
	s.svc.Audit(context.WithoutCancel(c.r.Context()), storeEntry(c, "FILE_GET", project, c.detail))
	return nil
}

// fileRaw sends the bytes of one regular file.
func (s *Server) fileRaw(c *rc, name, p string) error {
	ctx := c.r.Context()
	if p == "" {
		return api.Errorf(api.CodeInvalid, "name a file: the root of the jail is a directory").WithHint("?archive=tar&recursive=true sends a directory as a tar stream")
	}
	st, err := s.svc.JailFileStat(ctx, name, p)
	if err != nil {
		return err
	}
	switch st.Type {
	case api.FileTypeNone:
		return api.Errorf(api.CodeNotFound, "jail %q has no /%s", name, p)
	case api.FileTypeFile:
	case api.FileTypeDir:
		return api.Errorf(api.CodeInvalid, "/%s is a directory", p).WithHint("?archive=tar&recursive=true sends a directory as a tar stream")
	default:
		return api.Errorf(api.CodeInvalid, "/%s is a %s, not a regular file", p, st.Type).WithHint("?archive=tar sends a symlink as itself")
	}
	pr, pw := io.Pipe()
	type result struct {
		project string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		project, _, err := s.svc.JailFileGet(ctx, name, p, service.JailFileGetOpts{}, pw)
		pw.CloseWithError(err)
		done <- result{project, err}
	}()
	tr := tar.NewReader(pr)
	h, err := tr.Next()
	if err != nil {
		pr.CloseWithError(err)
		r := <-done
		if r.err != nil {
			return r.err
		}
		return api.Errorf(api.CodeInternal, "the file could not be read")
	}
	if h.Typeflag != tar.TypeReg {
		pr.Close()
		<-done
		return api.Errorf(api.CodeInvalid, "/%s is not a regular file", p)
	}
	c.w.Header().Set("Content-Type", "application/octet-stream")
	c.w.Header().Set("Content-Length", strconv.FormatInt(h.Size, 10))
	c.w.Header().Set(api.HeaderFileMode, fmt.Sprintf("%04o", h.Mode&0o777))
	c.w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", path.Base(p)))
	c.status = http.StatusOK
	c.w.WriteHeader(http.StatusOK)
	n, cerr := io.Copy(c.w, tr)
	pr.Close()
	r := <-done
	if cerr == nil {
		cerr = r.err
	}
	c.detail = fmt.Sprintf("file get /%s bytes=%d", p, n)
	s.svc.Audit(context.WithoutCancel(c.r.Context()), storeEntry(c, "FILE_GET", r.project, c.detail))
	if cerr != nil || n != h.Size {
		// the headers are out: the connection is cut so that the client sees a short body
		panic(http.ErrAbortHandler)
	}
	return nil
}

func (s *Server) handleFilePut(c *rc) error {
	name, p := c.pathArg("name"), c.pathArg("path")
	c.object = "/api/v1/jails/" + name
	q := c.r.URL.Query()
	s.lift(c)
	max := s.svc.FileCopyMaxBytes()
	flag := func(k string) bool { return q.Get(k) == "true" }
	opts := service.JailFilePutOpts{Mkdir: flag(api.FileQueryMkdir), Preserve: flag(api.FileQueryPreserve)}
	var in io.Reader
	dir := p
	switch a := q.Get(api.FileQueryArchive); a {
	case api.FileArchiveTar:
		// the archive has headers and padding on top of its data: the daemon cuts the stream a little above the limit,
		// the helper counts the data itself
		in = http.MaxBytesReader(c.w, c.r.Body, max+max/8+1<<20)
	case "":
		var err error
		if in, dir, err = s.rawPut(c, p, q.Get(api.FileQueryMode), max); err != nil {
			return err
		}
	default:
		return api.Errorf(api.CodeInvalid, "archive must be %q", api.FileArchiveTar)
	}
	project, st, err := s.svc.JailFilePut(c.r.Context(), name, dir, opts, in)
	c.project = project
	if err != nil {
		return err
	}
	c.detail = fmt.Sprintf("file put /%s files=%d dirs=%d symlinks=%d bytes=%d", p, st.Files, st.Dirs, st.Symlinks, st.Bytes)
	c.sync(200, api.JailFileCopy{Files: st.Files, Dirs: st.Dirs, Symlinks: st.Symlinks, Skipped: st.Skipped, Bytes: st.Bytes}, "")
	return nil
}

// rawPut turns the body of a plain PUT into a one-entry tar stream for the directory of the path.
func (s *Server) rawPut(c *rc, p, mode string, max int64) (io.Reader, string, error) {
	if p == "" {
		return nil, "", api.Errorf(api.CodeInvalid, "name the file to write").WithHint("write below a directory with ?archive=tar")
	}
	n := c.r.ContentLength
	if n < 0 {
		return nil, "", api.Errorf(api.CodeInvalid, "a plain upload needs a Content-Length").WithHint("send the length, or upload a tar stream with ?archive=tar")
	}
	if n > max {
		return nil, "", api.Errorf(api.CodeInvalid, "the file is %d bytes: one transfer carries at most %d", n, max).
			WithHint("limits.file_copy_bytes of jailopsd.yaml raises it")
	}
	perm := int64(0o644)
	if mode != "" {
		v, err := strconv.ParseUint(mode, 8, 32)
		if err != nil || v&^0o777 != 0 {
			return nil, "", api.Errorf(api.CodeInvalid, "mode %q must be octal permission bits (0644)", mode)
		}
		perm = int64(v)
	}
	dir, base := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	pr, pw := io.Pipe()
	body := c.r.Body
	go func() {
		tw := tar.NewWriter(pw)
		err := tw.WriteHeader(&tar.Header{Name: base, Mode: perm, Size: n, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
		if err == nil {
			_, err = io.CopyN(tw, body, n)
		}
		if err == nil {
			err = tw.Close()
		}
		pw.CloseWithError(err)
	}()
	return pr, dir, nil
}
