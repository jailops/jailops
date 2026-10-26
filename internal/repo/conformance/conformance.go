// Package conformance is the S3 conformance suite of the repository: every S3
// behaviour the repository code relies on, checked through the S3 client of
// internal/repo/s3 and through hand-signed raw requests.
//
// The suite runs in every normal `go test` against the in-process fake
// (internal/repo/s3fake), which keeps the fake honest, and against a real
// service when JAILOPS_TEST_S3_ENDPOINT, JAILOPS_TEST_S3_ACCESS_KEY and
// JAILOPS_TEST_S3_SECRET_KEY are set (see TargetFromEnv). Checks are plain
// functions, so mutation tests can run one against a deliberately broken fake
// and require it to FAIL. Nothing is silently skipped: a configured service
// that cannot be used fails the run.
package conformance

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3"
)

// Target is the service under test.
type Target struct {
	Endpoint  string // host:port (plain HTTP) or https://host:port
	Region    string
	AccessKey string
	SecretKey string
	// Bucket is an existing bucket to run in. Empty means every check creates
	// and removes its own bucket.
	Bucket string
	// PathStyle forces path-style addressing (default for any endpoint).
	VirtualHost bool
	CAPEM       string
	// MakeBuckets allows creating buckets on an external service (needed by
	// the datastore contract run).
	MakeBuckets bool
}

// TargetFromEnv reads JAILOPS_TEST_S3_ENDPOINT, _ACCESS_KEY, _SECRET_KEY and
// optionally _REGION, _BUCKET, _CA_FILE, _VIRTUAL_HOST=1 and _MAKE_BUCKETS=1.
// ok is false when no endpoint is configured. A configured endpoint with
// missing credentials is an error.
func TargetFromEnv() (t Target, ok bool, err error) {
	ep := os.Getenv("JAILOPS_TEST_S3_ENDPOINT")
	if ep == "" {
		return t, false, nil
	}
	t = Target{Endpoint: ep, Region: os.Getenv("JAILOPS_TEST_S3_REGION"), AccessKey: os.Getenv("JAILOPS_TEST_S3_ACCESS_KEY"),
		SecretKey: os.Getenv("JAILOPS_TEST_S3_SECRET_KEY"), Bucket: os.Getenv("JAILOPS_TEST_S3_BUCKET"),
		VirtualHost: os.Getenv("JAILOPS_TEST_S3_VIRTUAL_HOST") == "1", MakeBuckets: os.Getenv("JAILOPS_TEST_S3_MAKE_BUCKETS") == "1"}
	if t.AccessKey == "" || t.SecretKey == "" {
		return t, true, errors.New("JAILOPS_TEST_S3_ENDPOINT is set but JAILOPS_TEST_S3_ACCESS_KEY / _SECRET_KEY are not")
	}
	if f := os.Getenv("JAILOPS_TEST_S3_CA_FILE"); f != "" {
		b, rerr := os.ReadFile(f)
		if rerr != nil {
			return t, true, rerr
		}
		t.CAPEM = string(b)
	}
	if t.Region == "" {
		t.Region = "us-east-1"
	}
	return t, true, nil
}

func (t Target) secure() bool { return strings.HasPrefix(t.Endpoint, "https://") }
func (t Target) host() string {
	return strings.TrimPrefix(strings.TrimPrefix(t.Endpoint, "https://"), "http://")
}

// Config returns the client configuration for bucket and prefix.
func (t Target) Config(bucket, prefix string) s3.Config {
	c := s3.Config{Endpoint: t.Endpoint, Region: t.Region, Bucket: bucket, Prefix: prefix,
		Credentials: s3.Credentials{AccessKey: t.AccessKey, SecretKey: t.SecretKey}, NoTLS: !t.secure(), CAPEM: t.CAPEM,
		BackoffBase: 20 * time.Millisecond, BackoffMax: 500 * time.Millisecond, Attempts: 4, StallTimeout: 30 * time.Second}
	if t.Region == "" {
		c.Region = "us-east-1"
	}
	if t.VirtualHost {
		c.Addressing = s3.AddressingVirtual
	}
	return c
}

// Check is one named conformance check.
type Check struct {
	Name string
	Fn   func(c *C)
}

// C is the check's handle: Errorf records, Fatalf records and stops the check.
type C struct {
	Target Target
	Ctx    context.Context
	// B is a backend on a fresh bucket (or a fresh prefix of Target.Bucket).
	B      *s3.Backend
	Bucket string
	Prefix string

	mu    sync.Mutex
	fails []string
	rnd   *rand.Rand
	extra []*s3.Backend
	made  []string
}

type stop struct{}

// Errorf records a failure and goes on.
func (c *C) Errorf(format string, a ...any) {
	c.mu.Lock()
	c.fails = append(c.fails, fmt.Sprintf(format, a...))
	c.mu.Unlock()
}

// Fatalf records a failure and stops the check.
func (c *C) Fatalf(format string, a ...any) {
	c.Errorf(format, a...)
	panic(stop{})
}

func (c *C) must(err error, what string) {
	if err != nil {
		c.Fatalf("%s: %v", what, err)
	}
}

var bucketSeq atomic.Int64

func (c *C) data(n int) []byte {
	b := make([]byte, n)
	c.rnd.Read(b)
	return b
}

func (c *C) put(key string, data []byte, ifAbsent bool) error {
	return c.B.Put(c.Ctx, key, bytes.NewReader(data), int64(len(data)), datastore.PutOptions{IfAbsent: ifAbsent})
}

func (c *C) mustPut(key string, data []byte, ifAbsent bool) {
	c.must(c.put(key, data, ifAbsent), "put "+key)
}

func (c *C) get(key string) ([]byte, error) {
	rc, err := c.B.Get(c.Ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (c *C) mustGet(key string) []byte {
	d, err := c.get(key)
	c.must(err, "get "+key)
	return d
}

func (c *C) rng(key string, off, n int64) ([]byte, error) {
	rc, err := c.B.GetRange(c.Ctx, key, off, n)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (c *C) list(prefix string) []string {
	var keys []string
	err := c.B.List(c.Ctx, prefix, func(o datastore.ObjectInfo) error { keys = append(keys, o.Key); return nil })
	c.must(err, "list "+prefix)
	return keys
}

func (c *C) equalStrings(what string, got, want []string) {
	if len(got) != len(want) {
		c.Fatalf("%s: %d entries %v, want %d %v", what, len(got), head(got), len(want), head(want))
	}
	for i := range got {
		if got[i] != want[i] {
			c.Fatalf("%s: entry %d is %q, want %q", what, i, got[i], want[i])
		}
	}
}

func head(s []string) []string {
	if len(s) > 8 {
		return append(append([]string(nil), s[:8]...), "...")
	}
	return s
}

// parallel runs fn(i) for 0 <= i < n on `workers` goroutines and returns the first error.
func parallel(n, workers int, fn func(i int) error) error {
	var wg sync.WaitGroup
	var first atomic.Value
	idx := int64(-1)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(atomic.AddInt64(&idx, 1))
				if i >= n {
					return
				}
				if err := fn(i); err != nil {
					first.CompareAndSwap(nil, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	if e := first.Load(); e != nil {
		return e.(error)
	}
	return nil
}

// ---- raw requests ------------------------------------------------------------

// rawReq builds a hand-signed path-style request on this check's bucket. key is
// relative to the check's prefix; query values are signed as given.
func (c *C) rawReq(method, key string, query map[string]string, body io.Reader, size int64, payload string, at time.Time) *http.Request {
	scheme := "http"
	if c.Target.secure() {
		scheme = "https"
	}
	path := "/" + c.Bucket
	if key != "" {
		path += "/" + s3.EncodePath(c.Prefix+key)
	}
	u := scheme + "://" + c.Target.host() + path
	if q := s3.CanonicalQuery(query); q != "" {
		u += "?" + q
	}
	req, err := http.NewRequestWithContext(c.Ctx, method, u, body)
	c.must(err, "new request")
	if body != nil {
		req.ContentLength = size
	}
	s3.SignRequest(req, s3.Credentials{AccessKey: c.Target.AccessKey, SecretKey: c.Target.SecretKey}, c.Target.Region, payload, at)
	return req
}

func (c *C) client() *http.Client {
	// a private transport: the checks abort connections on purpose
	return &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: time.Minute}
}

func (c *C) do(req *http.Request) (int, string) {
	resp, err := c.client().Do(req)
	c.must(err, "raw request")
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, string(b)
}

// ---- running -----------------------------------------------------------------

func newBackend(t Target, bucket, prefix string) (*s3.Backend, error) {
	return s3.New(t.Config(bucket, prefix))
}

// Run executes one check against the target and returns its failures (nil = passed).
func Run(tgt Target, ch Check) (fails []string) {
	if tgt.Region == "" {
		tgt.Region = "us-east-1"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c := &C{Target: tgt, Ctx: ctx, rnd: rand.New(rand.NewSource(42))}
	defer func() {
		if r := recover(); r != nil {
			if _, ok := r.(stop); !ok {
				c.Errorf("panic: %v", r)
			}
		}
		c.cleanup()
		fails = c.fails
	}()
	if tgt.Bucket != "" {
		c.Bucket = tgt.Bucket
		c.Prefix = fmt.Sprintf("jailops-conf-%d-%d/", time.Now().UnixNano()%1_000_000_000, bucketSeq.Add(1))
	} else {
		c.Bucket = fmt.Sprintf("joconf-%d-%d", time.Now().UnixNano()%1_000_000_000, bucketSeq.Add(1))
		mk, err := newBackend(tgt, c.Bucket, "")
		if err != nil {
			return []string{err.Error()}
		}
		if err := mk.MakeBucket(ctx); err != nil {
			return []string{"make bucket: " + err.Error()}
		}
		c.made = append(c.made, c.Bucket)
	}
	b, err := newBackend(tgt, c.Bucket, c.Prefix)
	if err != nil {
		return []string{err.Error()}
	}
	c.B = b
	ch.Fn(c)
	return
}

func (c *C) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if c.B != nil {
		var keys []string
		_ = c.B.List(ctx, "", func(o datastore.ObjectInfo) error { keys = append(keys, o.Key); return nil })
		_ = parallel(len(keys), 8, func(i int) error { return c.B.Delete(ctx, keys[i]) })
	}
	for _, bk := range c.made {
		if b, err := newBackend(c.Target, bk, ""); err == nil {
			_ = b.RemoveBucket(ctx)
		}
	}
}

// RunConformance runs the whole suite as subtests of t.
func RunConformance(t *testing.T, tgt Target) {
	t.Helper()
	for _, ch := range Checks() {
		t.Run(ch.Name, func(t *testing.T) {
			for _, f := range Run(tgt, ch) {
				t.Error(f)
			}
		})
	}
}

// Lookup returns the check with that name.
func Lookup(name string) (Check, bool) {
	for _, ch := range Checks() {
		if ch.Name == name {
			return ch, true
		}
	}
	return Check{}, false
}

// Factory returns a datastore.RunConformance factory that creates a fresh
// bucket per call on the target (it needs permission to create buckets).
func Factory(tgt Target, cleanup func(func())) func(t *testing.T) datastore.Backend {
	return func(t *testing.T) datastore.Backend {
		t.Helper()
		name := fmt.Sprintf("jods-%d-%d", time.Now().UnixNano()%1_000_000_000, bucketSeq.Add(1))
		b, err := newBackend(tgt, name, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := b.MakeBucket(context.Background()); err != nil {
			t.Fatalf("the datastore contract needs buckets it may create: %v", err)
		}
		t.Cleanup(func() {
			ctx := context.Background()
			var keys []string
			_ = b.List(ctx, "", func(o datastore.ObjectInfo) error { keys = append(keys, o.Key); return nil })
			for _, k := range keys {
				_ = b.Delete(ctx, k)
			}
			_ = b.RemoveBucket(ctx)
			b.Close()
		})
		return b
	}
}
