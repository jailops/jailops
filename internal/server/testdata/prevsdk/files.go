package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// FileStat says what is at a path inside a jail; a missing path has the type "none" (extension jail_files). The path is
// as the jail sees it ("/etc/motd", a leading slash is allowed).
func (j *JailClient) FileStat(ctx context.Context, name, p string) (api.JailFileStat, error) {
	if err := j.c.need("jail_files"); err != nil {
		return api.JailFileStat{}, err
	}
	cp, err := api.CleanJailFilePath(p)
	if err != nil {
		return api.JailFileStat{}, err
	}
	st, _, err := get[api.JailFileStat](ctx, j.c, j.r.item(name)+"/files/"+escapePath(cp), url.Values{api.FileQueryStat: {"true"}})
	return st, err
}

// FileGetOptions are the options of FileGet.
type FileGetOptions struct {
	// Archive asks for a tar stream: of a regular file, a symlink or (with Recursive) a directory tree. Without it the
	// bytes of one regular file are sent.
	Archive   bool
	Recursive bool
	Preserve  bool
	// As names the entries of the archive below this name (default: the last element of the path).
	As string
}

// FileDownload is a download of FileGet. Reading it to its end returns io.EOF only when the transfer was complete: a
// download that failed after it had begun ends with the error the daemon gave. Close it.
type FileDownload struct {
	resp *http.Response
	// Mode is the permission bits of a plain download, in octal ("0644"); empty for a tar stream.
	Mode string
	// Size is the length of a plain download, -1 for a tar stream.
	Size int64
	err  error
}

// Read implements io.Reader.
func (d *FileDownload) Read(p []byte) (int, error) {
	n, err := d.resp.Body.Read(p)
	if errors.Is(err, io.EOF) {
		if msg := d.resp.Trailer.Get(api.HeaderFileError); msg != "" {
			d.err = api.Errorf(api.CodeInternal, "the download failed: %s", msg)
			return n, d.err
		}
	}
	return n, err
}

// Skipped is the number of devices, FIFOs and sockets a tree download left out; it is known after the stream is read to
// its end.
func (d *FileDownload) Skipped() int {
	n, _ := strconv.Atoi(d.resp.Trailer.Get(api.HeaderSkipped))
	return n
}

// Close ends the download.
func (d *FileDownload) Close() error { return d.resp.Body.Close() }

// FileGet downloads a path inside a jail (extension jail_files). The transfer is streamed: nothing is held in memory and
// no timeout of the client applies to it, only ctx.
func (j *JailClient) FileGet(ctx context.Context, name, p string, o FileGetOptions) (*FileDownload, error) {
	if err := j.c.need("jail_files"); err != nil {
		return nil, err
	}
	cp, err := api.CleanJailFilePath(p)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	if o.Archive {
		q.Set(api.FileQueryArchive, api.FileArchiveTar)
	}
	if o.Recursive {
		q.Set(api.FileQueryRecursive, "true")
	}
	if o.Preserve {
		q.Set(api.FileQueryPreserve, "true")
	}
	if o.As != "" {
		q.Set(api.FileQueryAs, o.As)
	}
	resp, err := j.c.stream(ctx, http.MethodGet, j.r.item(name)+"/files/"+escapePath(cp), q, nil, -1, "")
	if err != nil {
		return nil, err
	}
	d := &FileDownload{resp: resp, Size: resp.ContentLength}
	if !o.Archive {
		d.Mode = resp.Header.Get(api.HeaderFileMode)
	}
	return d, nil
}

// FilePutOptions are the options of FilePut.
type FilePutOptions struct {
	// Archive sends a tar stream that is written below the directory at the path. Without it Body is the content of
	// the one regular file at the path and Length is required.
	Archive bool
	// Length is the length of Body when Archive is false.
	Length int64
	// Mode is the permission bits of a plain upload (default 0644).
	Mode     uint32
	Mkdir    bool
	Preserve bool
}

// FilePut writes into a jail (extension jail_files): the one file at the path, or with Archive a tar stream below the
// directory at the path. The body is streamed.
func (j *JailClient) FilePut(ctx context.Context, name, p string, body io.Reader, o FilePutOptions) (api.JailFileCopy, error) {
	if err := j.c.need("jail_files"); err != nil {
		return api.JailFileCopy{}, err
	}
	cp, err := api.CleanJailFilePath(p)
	if err != nil {
		return api.JailFileCopy{}, err
	}
	q := url.Values{}
	length := int64(-1)
	ctype := api.MediaTar
	if o.Archive {
		q.Set(api.FileQueryArchive, api.FileArchiveTar)
	} else {
		if o.Length < 0 {
			return api.JailFileCopy{}, api.Errorf(api.CodeInvalid, "a plain upload needs its length")
		}
		length, ctype = o.Length, "application/octet-stream"
		if o.Mode != 0 {
			q.Set(api.FileQueryMode, fmt.Sprintf("%o", o.Mode))
		}
	}
	if o.Mkdir {
		q.Set(api.FileQueryMkdir, "true")
	}
	if o.Preserve {
		q.Set(api.FileQueryPreserve, "true")
	}
	resp, err := j.c.stream(ctx, http.MethodPut, j.r.item(name)+"/files/"+escapePath(cp), q, body, length, ctype)
	if err != nil {
		return api.JailFileCopy{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, j.c.t.cfg.maxBody+1))
	if err != nil {
		return api.JailFileCopy{}, err
	}
	env, err := decodeResponse(resp.StatusCode, data)
	if err != nil {
		return api.JailFileCopy{}, err
	}
	return decodeMeta[api.JailFileCopy](env.Metadata)
}

// escapePath escapes the segments of a path for a URL path.
func escapePath(p string) string {
	if p == "" {
		return ""
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// stream sends a request whose body or reply is a stream. A reply with a status of 300 or more is read as an error
// envelope and returned as the error; otherwise the response is returned open, for the caller to read and close. There
// is no retry and no timeout but ctx.
func (c *Client) stream(ctx context.Context, method, path string, q url.Values, body io.Reader, length int64, ctype string) (*http.Response, error) {
	if c.t.ctx.Err() != nil {
		return nil, ErrClosed
	}
	reqID := newRequestID()
	rctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.t.ctx, cancel)
	req, err := http.NewRequestWithContext(rctx, method, c.buildURL(path, q), body)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	if body != nil {
		req.ContentLength = length
		if length == 0 {
			req.Body = http.NoBody
		}
		req.Header.Set("Content-Type", ctype)
	}
	c.decorate(req, reqID)
	if req.Header.Get("Accept") == "application/json" && method == http.MethodGet {
		req.Header.Del("Accept")
	}
	resp, err := c.t.hc.Do(req)
	if err != nil {
		stop()
		cancel()
		if c.t.ctx.Err() != nil {
			return nil, ErrClosed
		}
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer func() { stop(); cancel(); resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, c.t.cfg.maxBody+1))
		_, derr := decodeResponse(resp.StatusCode, data)
		if derr == nil {
			derr = errorFromStatus(resp.StatusCode, data)
		}
		var ae *api.Error
		if errors.As(derr, &ae) && ae.Detail["request_id"] == "" {
			derr = ae.WithDetail("request_id", reqID)
		}
		return nil, derr
	}
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: func() { stop(); cancel() }}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel func()
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}
