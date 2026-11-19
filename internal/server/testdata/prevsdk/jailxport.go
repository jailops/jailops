package sdk

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"jailops.net/jailops/sdk/api"
)

// ExportJail writes the export file of a jail into w (GET /api/v1/jails/{name}/export, extension jail_export). The
// server ends the reply with a trailer that says whether the file is complete; a file that is not is an error here, and
// what was written is not a usable file.
func (j *JailClient) Export(ctx context.Context, name string, o api.JailExportRequest, w io.Writer) error {
	if err := j.c.need("jail_export"); err != nil {
		return err
	}
	q := url.Values{}
	if o.Snapshot != "" {
		q.Set("snapshot", o.Snapshot)
	}
	if o.WithVolumes {
		q.Set("with_volumes", "true")
	}
	if o.Zstd {
		q.Set("zstd", "true")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.c.buildURL(j.r.item(name)+"/export", q), nil)
	if err != nil {
		return err
	}
	j.c.decorate(req, newRequestID())
	req.Header.Set("Accept", "application/vnd.jailops.export")
	resp, err := j.c.streamClient().Do(req)
	if err != nil {
		if j.c.t.ctx.Err() != nil {
			return ErrClosed
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_, derr := decodeResponse(resp.StatusCode, data)
		if derr == nil {
			derr = api.Errorf(api.CodeInternal, "the export answered status %d", resp.StatusCode)
		}
		return derr
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return err
	}
	switch t := resp.Trailer.Get("X-Jailops-Export"); {
	case t == "ok":
		return nil
	case strings.HasPrefix(t, "error: "):
		return api.Errorf(api.CodeInternal, "%s", strings.TrimPrefix(t, "error: "))
	}
	return errors.New("jailops: the export ended without the server's confirmation: the file is not complete")
}

// ImportPreflight asks what an import of a file with this manifest would do (POST /api/v1/jail-imports?dry_run=true). It
// changes nothing.
func (j *JailClient) ImportPreflight(ctx context.Context, in api.JailImportPreflight) (api.RestorePreflight, error) {
	if err := j.c.need("jail_export"); err != nil {
		return api.RestorePreflight{}, err
	}
	v, _, err := send[api.RestorePreflight](ctx, j.c, request{method: http.MethodPost, path: "/api/v1/jail-imports", query: url.Values{"dry_run": {"true"}}, body: in})
	return v, err
}

// Import makes a jail of the export file read from r (POST /api/v1/jail-imports) and returns it. The request lasts
// as long as the file takes to send and the jail to be made; a file that is cut off or damaged leaves no jail behind.
func (j *JailClient) Import(ctx context.Context, o api.JailImportRequest, r io.Reader) (api.JailImported, error) {
	if err := j.c.need("jail_export"); err != nil {
		return api.JailImported{}, err
	}
	q := url.Values{}
	if o.As != "" {
		q.Set("as", o.As)
	}
	if o.KeepIP {
		q.Set("keep_ip", "true")
	}
	if o.Start {
		q.Set("start", "true")
	}
	for _, k := range sortedKeys(o.NetworkMap) {
		q.Add("network_map", k+"="+o.NetworkMap[k])
	}
	for _, k := range sortedKeys(o.VolumeMap) {
		q.Add("volume_map", k+"="+o.VolumeMap[k])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, j.c.buildURL("/api/v1/jail-imports", q), r)
	if err != nil {
		return api.JailImported{}, err
	}
	j.c.decorate(req, newRequestID())
	req.Header.Set("Content-Type", "application/vnd.jailops.export")
	resp, err := j.c.streamClient().Do(req)
	if err != nil {
		if j.c.t.ctx.Err() != nil {
			return api.JailImported{}, ErrClosed
		}
		return api.JailImported{}, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	env, derr := decodeResponse(resp.StatusCode, data)
	if derr != nil {
		return api.JailImported{}, derr
	}
	op, err := decodeMeta[api.Operation](env.Metadata)
	if err != nil {
		return api.JailImported{}, err
	}
	if op.Error != nil {
		return api.JailImported{}, op.Error
	}
	return decodeMeta[api.JailImported](op.Metadata)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for k := i; k > 0 && out[k] < out[k-1]; k-- {
			out[k], out[k-1] = out[k-1], out[k]
		}
	}
	return out
}
