package sdk

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"jailops.net/jailops/sdk/api"
)

// LogsClient reads the logs of many jails at once (extension logs_unified)
// and manages where the host forwards them (extension log_forward).
type LogsClient struct{ c *Client }

// Logs returns the logs client.
func (c *Client) Logs() *LogsClient { return &LogsClient{c} }

// Read merges the ends of the selected jails' log files into one stream
// ordered by time (GET /api/v1/logs, operator). The client's project is
// read, or with allProjects every project the caller may use. Send Next of
// the reply as q.Cursor to get what was written afterwards.
func (l *LogsClient) Read(ctx context.Context, q api.LogsQuery, allProjects bool) (api.LogsRead, error) {
	if err := l.c.need("logs_unified"); err != nil {
		return api.LogsRead{}, err
	}
	q, err := q.Normalize()
	if err != nil {
		return api.LogsRead{}, err
	}
	v := q.Values()
	if allProjects {
		v.Set("all_projects", "true")
	}
	r, _, err := get[api.LogsRead](ctx, l.c, "/api/v1/logs", v)
	if r.Lines == nil {
		r.Lines = []api.LogEntry{}
	}
	if r.Sources == nil {
		r.Sources = []api.LogSource{}
	}
	return r, err
}

// Forward reads the sinks and their state (GET /api/v1/logs/forward, admin).
func (l *LogsClient) Forward(ctx context.Context) (api.LogForward, error) {
	if err := l.c.need("log_forward"); err != nil {
		return api.LogForward{}, err
	}
	v, _, err := get[api.LogForward](ctx, l.c, "/api/v1/logs/forward", nil)
	return v, err
}

// SetForward stores the sink list, which then replaces logging.forward of
// jailopsd.yaml (PUT /api/v1/logs/forward, admin). expect is the generation
// read before (Status.Generation; 0 while the file's list is in force) and
// fails with api.CodeConflict when another change came first; a negative
// expect overwrites.
func (l *LogsClient) SetForward(ctx context.Context, spec api.LogForwardSpec, expect int64) (api.LogForward, error) {
	if err := l.c.need("log_forward"); err != nil {
		return api.LogForward{}, err
	}
	h := http.Header{}
	if expect >= 0 {
		h.Set("If-Match", quoteETag(`"`+strconv.FormatInt(expect, 10)+`"`))
	}
	v, _, err := send[api.LogForward](ctx, l.c, request{method: http.MethodPut, path: "/api/v1/logs/forward", body: spec, header: h})
	return v, err
}

// ResetForward deletes the stored sink list: logging.forward of
// jailopsd.yaml is in force again (DELETE /api/v1/logs/forward, admin).
func (l *LogsClient) ResetForward(ctx context.Context) (api.LogForward, error) {
	if err := l.c.need("log_forward"); err != nil {
		return api.LogForward{}, err
	}
	v, _, err := send[api.LogForward](ctx, l.c, request{method: http.MethodDelete, path: "/api/v1/logs/forward"})
	return v, err
}

// TestForward sends one tagged line to a sink and reports how it went
// (POST /api/v1/logs/forward/test, admin). A failed send is a result with
// OK false, not an error.
func (l *LogsClient) TestForward(ctx context.Context, sink string) (api.LogForwardTestResult, error) {
	if err := l.c.need("log_forward"); err != nil {
		return api.LogForwardTestResult{}, err
	}
	v, _, err := send[api.LogForwardTestResult](ctx, l.c, request{method: http.MethodPost, path: "/api/v1/logs/forward/test",
		body: api.LogForwardTest{Sink: sink}, timeout: 30 * time.Second})
	return v, err
}
