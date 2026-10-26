package sdk

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

type request struct {
	method  string
	path    string
	query   url.Values
	body    any
	header  http.Header
	timeout time.Duration // overrides the client default when > 0
}

func newRequestID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (c *Client) userAgent() string {
	ua := fmt.Sprintf("jailops-sdk/%s (go%s; %s/%s)", Version, strings.TrimPrefix(runtime.Version(), "go"), runtime.GOOS, runtime.GOARCH)
	if c.t.cfg.userAgent != "" {
		ua += " " + c.t.cfg.userAgent
	}
	return ua
}

func (c *Client) buildURL(path string, q url.Values) string {
	if proj := c.ProjectName(); c.host != "" || proj != "" {
		nq := url.Values{}
		for k, v := range q {
			nq[k] = v
		}
		if c.host != "" {
			nq.Set("host", c.host)
		}
		if proj != "" && nq.Get("project") == "" {
			nq.Set("project", proj)
		}
		q = nq
	}
	u := c.t.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func isIdempotent(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// do sends a request and returns the decoded envelope of a successful reply.
// Failures are *api.Error where the server answered, and transport errors
// otherwise. Only idempotent requests are retried.
func (c *Client) do(ctx context.Context, r request) (*api.Response, http.Header, error) {
	if c.t.ctx.Err() != nil {
		return nil, nil, ErrClosed
	}
	var payload []byte
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return nil, nil, fmt.Errorf("jailops: encode request: %w", err)
		}
		payload = b
	}
	reqID := newRequestID()
	target := c.buildURL(r.path, r.query)
	attempts := 1
	if isIdempotent(r.method) {
		attempts += max(c.t.cfg.retries, 0)
	}
	for i := 0; ; i++ {
		resp, hdr, err, retry, after := c.attempt(ctx, r, target, payload, reqID)
		if err == nil {
			return resp, hdr, nil
		}
		if !retry || i+1 >= attempts || ctx.Err() != nil {
			return nil, nil, err
		}
		d := c.backoffFor(i, after)
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-c.t.ctx.Done():
			return nil, nil, ErrClosed
		}
	}
}

func (c *Client) backoffFor(i int, after time.Duration) time.Duration {
	cfg := c.t.cfg
	ceil := cfg.backoff << min(i, 16)
	if ceil <= 0 || ceil > cfg.backoffMax {
		ceil = cfg.backoffMax
	}
	d := time.Duration(mrand.Int64N(int64(ceil) + 1)) // full jitter
	if after > d {
		d = min(after, 10*time.Second)
	}
	return d
}

func (c *Client) attempt(ctx context.Context, r request, target string, payload []byte, reqID string) (resp *api.Response, hdr http.Header, err error, retry bool, after time.Duration) {
	timeout := c.t.cfg.timeout
	if r.timeout > 0 {
		timeout = r.timeout
	}
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.t.ctx, cancel)
	defer stop()
	if timeout > 0 {
		var cancelT context.CancelFunc
		rctx, cancelT = context.WithTimeout(rctx, timeout)
		defer cancelT()
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(rctx, r.method, target, body)
	if err != nil {
		return nil, nil, err, false, 0
	}
	c.decorate(req, reqID)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range r.header {
		req.Header[k] = v
	}
	hr, err := c.t.hc.Do(req)
	if err != nil {
		if c.t.ctx.Err() != nil {
			return nil, nil, ErrClosed, false, 0
		}
		var cv *tls.CertificateVerificationError
		fatal := ctx.Err() != nil || errors.Is(err, errPinMismatch) || errors.As(err, &cv)
		return nil, nil, err, !fatal, 0
	}
	defer hr.Body.Close()
	limit := c.t.cfg.maxBody
	data, err := io.ReadAll(io.LimitReader(hr.Body, limit+1))
	if err != nil {
		if c.t.ctx.Err() != nil {
			return nil, nil, ErrClosed, false, 0
		}
		return nil, nil, err, ctx.Err() == nil, 0
	}
	if int64(len(data)) > limit {
		return nil, nil, api.Errorf(api.CodeInternal, "response exceeds the %d byte limit", limit), false, 0
	}
	resp, derr := decodeResponse(hr.StatusCode, data)
	if derr != nil {
		if e, ok := derr.(*api.Error); ok {
			if e.Detail["request_id"] == "" {
				derr = e.WithDetail("request_id", reqID)
			}
		}
		switch hr.StatusCode {
		case 429, 502, 503, 504:
			if s, err := strconv.Atoi(hr.Header.Get("Retry-After")); err == nil && s > 0 {
				after = time.Duration(s) * time.Second
			}
			return nil, nil, derr, true, after
		}
		return nil, nil, derr, false, 0
	}
	return resp, hr.Header, nil, false, 0
}

func (c *Client) decorate(req *http.Request, reqID string) {
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("X-Request-ID", reqID)
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}
	if c.t.cfg.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.t.cfg.token)
	}
}

func decodeResponse(status int, body []byte) (*api.Response, error) {
	var env api.Response
	jerr := json.Unmarshal(body, &env)
	if jerr == nil && env.Type != "" {
		if env.Type == api.ResponseError || env.Error != nil || status >= 300 {
			e := env.Error
			if e == nil {
				return nil, errorFromStatus(status, body)
			}
			cp := *e
			if cp.Code == "" {
				cp.Code = codeForStatus(status)
			}
			return nil, &cp
		}
		return &env, nil
	}
	if status >= 300 {
		return nil, errorFromStatus(status, body)
	}
	if status == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return &api.Response{Type: api.ResponseSync, StatusCode: status}, nil
	}
	return nil, api.Errorf(api.CodeInternal, "server sent a reply that is not an API envelope")
}

func decodeMeta[T any](raw json.RawMessage) (T, error) {
	var v T
	if len(raw) == 0 || string(raw) == "null" {
		return v, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("jailops: decode %T: %w", v, err)
	}
	return v, nil
}

// get fetches a sync resource.
func get[T any](ctx context.Context, c *Client, path string, q url.Values) (T, http.Header, error) {
	return send[T](ctx, c, request{method: http.MethodGet, path: path, query: q})
}

// send performs a request whose reply is sync and decodes metadata as T.
// An async reply is waited for and its operation result decoded as T.
func send[T any](ctx context.Context, c *Client, r request) (T, http.Header, error) {
	var zero T
	op, hdr, err := sendOp[T](ctx, c, r, nil)
	if err != nil {
		return zero, hdr, err
	}
	v, err := op.Wait(ctx)
	return v, hdr, err
}

// sendOp performs a request whose reply may be sync or async and returns an
// Operation either way (a sync reply yields an operation that is already
// successful). fetch loads the result when an operation carries none.
func sendOp[T any](ctx context.Context, c *Client, r request, fetch func(context.Context) (T, error)) (*Operation[T], http.Header, error) {
	resp, hdr, err := c.do(ctx, r)
	if err != nil {
		return nil, nil, err
	}
	op := &Operation[T]{c: c, fetch: fetch}
	if resp.Type == api.ResponseAsync {
		op.op, err = decodeMeta[api.Operation](resp.Metadata)
		if err != nil {
			return nil, nil, err
		}
		if op.op.ID == "" {
			op.op.ID = lastSegment(resp.Operation)
			op.op.Status = api.OpPending
		}
		if op.op.ID == "" {
			return nil, nil, api.Errorf(api.CodeInternal, "async reply without operation")
		}
		return op, hdr, nil
	}
	op.op = api.Operation{Status: api.OpSuccess, Metadata: resp.Metadata}
	return op, hdr, nil
}

func lastSegment(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return p
}

func esc(s string) string { return url.PathEscape(s) }

func etagOf(h http.Header) string { return h.Get("ETag") }

func quoteETag(e string) string {
	if e == "" || strings.HasPrefix(e, `"`) || strings.HasPrefix(e, "W/") {
		return e
	}
	return `"` + e + `"`
}

// streamClient returns an HTTP client for upgraded connections: HTTP/1.1
// only (an upgrade does not exist in HTTP/2), no timeout, same trust.
func (c *Client) streamClient() *http.Client {
	c.t.streamOnce.Do(func() {
		tr := c.t.tr.Clone()
		tr.ForceAttemptHTTP2 = false
		tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
		if tr.TLSClientConfig != nil {
			cfg := tr.TLSClientConfig.Clone()
			cfg.NextProtos = []string{"http/1.1"}
			tr.TLSClientConfig = cfg
		}
		c.t.streamHC = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	})
	return c.t.streamHC
}
