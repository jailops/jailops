package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Backend is an S3 bucket (or a prefix of it) as a datastore.Backend. It is
// safe for concurrent use; the caller provides the parallelism.
type Backend struct {
	cfg    Config
	tgt    target
	hc     *http.Client
	tr     *http.Transport // nil when Config.HTTPClient is used
	prefix string          // "" or ends in "/"

	attempts int
	base     time.Duration
	maxWait  time.Duration
	stall    time.Duration
	maxBuf   int64
	ua       string
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
}

// New validates cfg and returns a Backend. No request is made.
func New(cfg Config) (*Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	t, err := resolveTarget(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	b := &Backend{cfg: cfg, tgt: t, attempts: cfg.Attempts, base: cfg.BackoffBase, maxWait: cfg.BackoffMax, stall: cfg.StallTimeout,
		maxBuf: cfg.MaxBufferedPut, ua: cfg.UserAgent, now: cfg.Now, sleep: cfg.Sleep}
	if b.attempts <= 0 {
		b.attempts = 6
	}
	if b.base <= 0 {
		b.base = 100 * time.Millisecond
	}
	if b.maxWait <= 0 {
		b.maxWait = 5 * time.Second
	}
	if b.stall <= 0 {
		b.stall = 60 * time.Second
	}
	if b.maxBuf <= 0 {
		b.maxBuf = 256 << 20
	}
	if b.ua == "" {
		b.ua = "jailops-s3"
	}
	if b.now == nil {
		b.now = time.Now
	}
	if b.sleep == nil {
		b.sleep = sleepCtx
	}
	if cfg.HTTPClient != nil {
		b.hc = cfg.HTTPClient
	} else {
		tr, err := newTransport(cfg)
		if err != nil {
			return nil, err
		}
		b.tr = tr
		b.hc = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if p := strings.Trim(cfg.Prefix, "/"); p != "" {
		b.prefix = p + "/"
	}
	return b, nil
}

// Close releases idle connections.
func (b *Backend) Close() {
	if b.tr != nil {
		b.tr.CloseIdleConnections()
	}
}

// Kind implements datastore.Backend.
func (b *Backend) Kind() string { return "s3" }

// Describe names the target without any secret.
func (b *Backend) Describe() string { return b.cfg.String() }

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// backoff is full jitter on an exponentially growing ceiling.
func (b *Backend) backoff(attempt int, retryAfter time.Duration) time.Duration {
	ceil := b.base << min(attempt, 20)
	if ceil <= 0 || ceil > b.maxWait {
		ceil = b.maxWait
	}
	d := time.Duration(rand.Int64N(int64(ceil) + 1))
	if d < b.base/2 {
		d = b.base / 2
	}
	if retryAfter > d {
		d = min(retryAfter, 30*time.Second)
	}
	return d
}

// spec describes one logical request.
type spec struct {
	op     string
	method string
	key    string            // repository key, for error messages
	full   string            // key on the service (prefix included); "" addresses the bucket
	params map[string]string // query
	header http.Header
	// body returns a fresh reader for every attempt; nil for no body.
	body func() (io.Reader, error)
	size int64
	// payload is the x-amz-content-sha256 value; "" means the empty payload.
	payload string
	// ok lists the non-2xx statuses that are returned as responses, not errors.
	ok map[int]bool

	// ambiguous is set when an attempt failed after the request may have taken effect.
	ambiguous bool
}

// location returns the host and the escaped path of the request.
func (b *Backend) location(sp *spec) (host, rawPath string) {
	if b.tgt.virtual {
		return b.cfg.Bucket + "." + b.tgt.host, "/" + encodePath(sp.full)
	}
	p := "/" + uriEncode(b.cfg.Bucket, false)
	if sp.full != "" {
		p += "/" + encodePath(sp.full)
	}
	return b.tgt.host, p
}

// fullKey is the key on the service.
func (b *Backend) fullKey(k string) string { return b.prefix + k }

// do performs a request with retries. The returned response has a status in
// 2xx or in sp.ok; its body must be closed by the caller. Any other answer is
// returned as *Error.
func (b *Backend) do(ctx context.Context, sp *spec) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, retryAfter, err := b.once(ctx, sp)
		if err == nil {
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt+1 >= b.attempts || !b.retryable(err) {
			if attempt > 0 {
				return nil, fmt.Errorf("%w (after %d attempts)", err, attempt+1)
			}
			return nil, err
		}
		if serr := b.sleep(ctx, b.backoff(attempt, retryAfter)); serr != nil {
			return nil, serr
		}
	}
}

func (b *Backend) retryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Temporary()
	}
	var ne *netError
	return errors.As(err, &ne)
}

// netError marks a transport failure (connection reset, timeout, stall): the
// request may or may not have reached the service.
type netError struct{ err error }

func (e *netError) Error() string { return e.err.Error() }
func (e *netError) Unwrap() error { return e.err }

// watchdog cancels a request that makes no progress.
type watchdog struct {
	t      *time.Timer
	d      time.Duration
	cancel context.CancelFunc
}

func newWatchdog(parent context.Context, d time.Duration) (context.Context, *watchdog) {
	ctx, cancel := context.WithCancel(parent)
	w := &watchdog{d: d, cancel: cancel}
	w.t = time.AfterFunc(d, cancel)
	return ctx, w
}

func (w *watchdog) kick() { w.t.Reset(w.d) }
func (w *watchdog) stop() { w.t.Stop(); w.cancel() }

// kickReader resets the watchdog on every read.
type kickReader struct {
	r io.Reader
	w *watchdog
}

func (k *kickReader) Read(p []byte) (int, error) {
	n, err := k.r.Read(p)
	k.w.kick()
	return n, err
}

// bodyCloser ends the attempt when the response body is closed.
type bodyCloser struct {
	io.Reader
	body io.Closer
	w    *watchdog
	once sync.Once
}

func (c *bodyCloser) Close() error {
	var err error
	c.once.Do(func() { err = c.body.Close(); c.w.stop() })
	return err
}

func (b *Backend) once(ctx context.Context, sp *spec) (*http.Response, time.Duration, error) {
	actx, wd := newWatchdog(ctx, b.stall)
	fail := func(err error) (*http.Response, time.Duration, error) {
		wd.stop()
		return nil, 0, err
	}
	host, rawPath := b.location(sp)
	u := buildURL(b.tgt.scheme, host, rawPath, sp.params)
	var body io.Reader
	if sp.body != nil {
		r, err := sp.body()
		if err != nil {
			return fail(err)
		}
		body = &kickReader{r: r, w: wd}
	}
	req, err := http.NewRequestWithContext(actx, sp.method, u.String(), nil)
	if err != nil {
		return fail(err)
	}
	req.URL = u
	req.Host = u.Host
	for k, v := range sp.header {
		req.Header[k] = v
	}
	req.Header.Set("User-Agent", b.ua)
	if body != nil {
		req.Body = io.NopCloser(body)
		req.ContentLength = sp.size
		if sp.size == 0 {
			req.Body = http.NoBody
		}
	}
	payload := sp.payload
	if payload == "" {
		payload = emptyPayload
	}
	signV4(req, b.cfg.Credentials, b.cfg.Region, payload, b.now())
	resp, err := b.hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			wd.stop()
			return nil, 0, ctx.Err()
		}
		if body != nil || sp.method == http.MethodPut || sp.method == http.MethodDelete {
			sp.ambiguous = true
		}
		return fail(&netError{fmt.Errorf("s3 %s %s: %w", sp.op, sp.key, scrub(err))})
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 || sp.ok[resp.StatusCode] {
		resp.Body = &bodyCloser{Reader: &kickReader{r: resp.Body, w: wd}, body: resp.Body, w: wd}
		return resp, 0, nil
	}
	ra := retryAfter(resp)
	e := parseError(sp.op, sp.key, resp)
	wd.stop()
	if e.Temporary() && sp.method != http.MethodGet && sp.method != http.MethodHead {
		sp.ambiguous = true
	}
	return nil, ra, e
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

// scrub removes the request URL from a transport error: URLs carry no
// secrets here (signing is header based), but the error text stays short.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
