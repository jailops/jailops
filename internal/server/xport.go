package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// The export and import of jails (docs/export-import.md). The file is a stream, so these handlers do not use the JSON
// envelope for their data: an export answers with the file, an import reads it from the body.

// importIdle is how long an import waits for the next bytes of the file before it gives up.
var importIdle = 2 * time.Minute

// deadlineReader renews the read deadline of the connection before every read: the server's read timeout bounds a whole
// request, and an upload of gigabytes is longer than that, but a client that stops sending is still cut off.
type deadlineReader struct {
	r    io.Reader
	ctl  *http.ResponseController
	idle time.Duration
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	_ = d.ctl.SetReadDeadline(time.Now().Add(d.idle))
	return d.r.Read(p)
}

// countWriter counts what an export wrote.
type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

func queryBool(q url.Values, k string) bool { return q.Get(k) == "true" || q.Get(k) == "1" }

// ExportTrailer is the HTTP trailer that ends an export reply: "ok", or "error: ..." when the file is not complete.
const exportTrailer = "X-Jailops-Export"

func (s *Server) handleExport(c *rc) error {
	q := c.r.URL.Query()
	req := api.JailExportRequest{Snapshot: q.Get("snapshot"), WithVolumes: queryBool(q, "with_volumes"), Zstd: queryBool(q, "zstd")}
	name := c.pathArg("name")
	c.object = "/api/v1/jails/" + name
	ctl := http.NewResponseController(c.w)
	// the file is written for as long as it takes, as long as the client takes it
	_ = ctl.SetWriteDeadline(time.Time{})
	// the headers go out with the first byte of the file: an error before that is an ordinary error reply, one after it
	// is the trailer of the reply (and, the footer missing, a file that no reader accepts)
	fw := &firstWrite{w: c.w, set: func() {
		h := c.w.Header()
		h.Set("Content-Type", "application/vnd.jailops.export")
		h.Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+".jailexport"))
		h.Set("Cache-Control", "no-store")
		h.Set("Trailer", exportTrailer)
		c.status = http.StatusOK
		c.w.WriteHeader(http.StatusOK)
	}}
	cw := &countWriter{w: fw}
	sum, err := s.svc.ExportJail(c.r.Context(), name, req, cw)
	detail := "export " + sum + fmt.Sprintf(" bytes=%d", cw.n)
	if err != nil {
		detail = fmt.Sprintf("export failed after %d bytes: %v", cw.n, err)
	}
	c.detail = detail
	if err != nil && !fw.started {
		return err
	}
	if err != nil {
		var ae *api.Error
		msg := "the export failed; see the daemon log for request " + c.rid
		if errors.As(err, &ae) {
			msg = ae.Message
		}
		s.log.Warn("export of a jail ended with an error", "jail", name, "rid", c.rid, "err", err)
		c.w.Header().Set(exportTrailer, "error: "+msg)
	} else {
		c.w.Header().Set(exportTrailer, "ok")
	}
	s.svc.Audit(context.WithoutCancel(c.r.Context()), storeEntry(c, "EXPORT", c.project, detail))
	return nil
}

// firstWrite sends the response headers when the first byte is written.
type firstWrite struct {
	w       http.ResponseWriter
	set     func()
	started bool
}

func (f *firstWrite) Write(p []byte) (int, error) {
	if !f.started {
		f.started = true
		f.set()
	}
	return f.w.Write(p)
}

func (s *Server) handleImportPreflight(c *rc) error {
	var in api.JailImportPreflight
	if err := c.decode(&in); err != nil {
		return err
	}
	pf, err := s.svc.ImportPreflight(c.r.Context(), in)
	if err != nil {
		return err
	}
	c.sync(200, pf, 0)
	return nil
}

func (s *Server) handleImport(c *rc) error {
	q := c.r.URL.Query()
	req := api.JailImportRequest{As: q.Get("as"), KeepIP: queryBool(q, "keep_ip"), Start: queryBool(q, "start"),
		NetworkMap: pairs(q["network_map"]), VolumeMap: pairs(q["volume_map"])}
	ct := c.r.Header.Get("Content-Type")
	if ct != "application/vnd.jailops.export" && ct != "application/octet-stream" {
		return api.Errorf(api.CodeInvalid, "the body of an import is the export file (Content-Type: application/vnd.jailops.export)")
	}
	ctl := http.NewResponseController(c.w)
	body := &deadlineReader{r: c.r.Body, ctl: ctl, idle: importIdle}
	c.object = "/api/v1/jail-imports"
	j, warnings, err := s.svc.ImportJail(c.r.Context(), req, body)
	if err != nil {
		return err
	}
	c.object = "/api/v1/jails/" + j.Metadata.Name
	c.detail = "import as " + j.Metadata.Name
	c.w.Header().Set("Location", "/api/v1/jails/"+j.Metadata.Name)
	c.sync(http.StatusCreated, api.JailImported{Jail: j, Warnings: warnings}, j.Metadata.Generation)
	return nil
}

// pairs reads OLD=NEW values.
func pairs(l []string) map[string]string {
	if len(l) == 0 {
		return nil
	}
	m := map[string]string{}
	for _, v := range l {
		if k, to, ok := strings.Cut(v, "="); ok {
			m[k] = to
		}
	}
	return m
}
