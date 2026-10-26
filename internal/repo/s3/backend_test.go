package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3fake"
)

const testBucket = "jailops-test"

func testConfig(srv *s3fake.Server) Config {
	return Config{
		Endpoint:    srv.Endpoint(),
		Region:      srv.Region(),
		Bucket:      testBucket,
		Credentials: Credentials{AccessKey: srv.AccessKey(), SecretKey: srv.SecretKey()},
		NoTLS:       true,
		BackoffBase: time.Millisecond,
		BackoffMax:  5 * time.Millisecond,
	}
}

func newFake(t *testing.T, opts ...s3fake.Options) (*s3fake.Server, *Backend) {
	t.Helper()
	var o s3fake.Options
	if len(opts) > 0 {
		o = opts[0]
	}
	srv, err := s3fake.New(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	if err := srv.EnsureBucket(testBucket); err != nil {
		t.Fatal(err)
	}
	b, err := New(testConfig(srv))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return srv, b
}

func put(t *testing.T, b *Backend, key string, data []byte, ifAbsent bool) error {
	t.Helper()
	return b.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), datastore.PutOptions{IfAbsent: ifAbsent})
}

func mustGet(t *testing.T, b *Backend, key string) []byte {
	t.Helper()
	rc, err := b.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	d, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestRoundtrip(t *testing.T) {
	_, b := newFake(t)
	ctx := context.Background()
	for _, n := range []int{0, 1, 100, 64 << 10, 3<<20 + 17} {
		key := fmt.Sprintf("obj/%d", n)
		data := randBytes(n)
		if err := put(t, b, key, data, true); err != nil {
			t.Fatalf("put %d: %v", n, err)
		}
		if got := mustGet(t, b, key); !bytes.Equal(got, data) {
			t.Fatalf("roundtrip of %d bytes differs", n)
		}
		info, err := b.Head(ctx, key)
		if err != nil || info.Size != int64(n) || info.Key != key {
			t.Fatalf("head: %+v %v", info, err)
		}
		if d := time.Since(info.ModTime); d < -time.Minute || d > time.Minute {
			t.Fatalf("modtime %v is not recent", info.ModTime)
		}
	}
	if _, err := b.Head(ctx, "absent"); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("head of a missing key: %v", err)
	}
	if _, err := b.Get(ctx, "absent"); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("get of a missing key: %v", err)
	}
}

func TestIfAbsent(t *testing.T) {
	_, b := newFake(t)
	if err := put(t, b, "k", []byte("one"), true); err != nil {
		t.Fatal(err)
	}
	if err := put(t, b, "k", []byte("two"), true); !errors.Is(err, datastore.ErrExists) {
		t.Fatalf("second create-only put: %v", err)
	}
	if got := mustGet(t, b, "k"); string(got) != "one" {
		t.Fatalf("a refused create-only put changed the object: %q", got)
	}
	if err := put(t, b, "k", []byte("three"), false); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, b, "k"); string(got) != "three" {
		t.Fatalf("plain put did not replace: %q", got)
	}
}

func TestIfAbsentRace(t *testing.T) {
	_, b := newFake(t)
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := put(t, b, "race", []byte(fmt.Sprintf("writer-%02d", i)), true)
			switch {
			case err == nil:
				wins.Add(1)
			case !errors.Is(err, datastore.ErrExists):
				t.Errorf("writer %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d writers won, want 1", wins.Load())
	}
}

func TestRange(t *testing.T) {
	_, b := newFake(t)
	ctx := context.Background()
	data := randBytes(1000)
	if err := put(t, b, "r", data, true); err != nil {
		t.Fatal(err)
	}
	read := func(off, n int64) ([]byte, error) {
		rc, err := b.GetRange(ctx, "r", off, n)
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	for _, c := range []struct{ off, n int64 }{{0, 1}, {10, 20}, {999, 1}, {500, -1}, {0, -1}, {990, 100}} {
		got, err := read(c.off, c.n)
		if err != nil {
			t.Fatalf("range %+v: %v", c, err)
		}
		end := int64(1000)
		if c.n >= 0 {
			end = min(c.off+c.n, 1000)
		}
		if !bytes.Equal(got, data[c.off:end]) {
			t.Fatalf("range %+v returned %d bytes, want %d", c, len(got), end-c.off)
		}
	}
	if got, err := read(5, 0); err != nil || len(got) != 0 {
		t.Fatalf("zero-length range: %d bytes, %v", len(got), err)
	}
	if _, err := read(1000, 1); err == nil || errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("a range past the end must be an error other than not-found, got %v", err)
	}
	if _, err := b.GetRange(ctx, "nope", 0, 5); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("range of a missing key: %v", err)
	}
	if _, err := b.GetRange(ctx, "nope", 0, 0); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("empty range of a missing key: %v", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	_, b := newFake(t)
	ctx := context.Background()
	if err := b.Delete(ctx, "never-existed"); err != nil {
		t.Fatal(err)
	}
	_ = put(t, b, "d", []byte("x"), true)
	if err := b.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, "d"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Head(ctx, "d"); !errors.Is(err, datastore.ErrNotFound) {
		t.Fatal(err)
	}
	// create-only works again after a delete
	if err := put(t, b, "d", []byte("y"), true); err != nil {
		t.Fatal(err)
	}
}

func TestListPaginationAndOrder(t *testing.T) {
	_, b := newFake(t)
	ctx := context.Background()
	var want []string
	for i := range 2503 {
		k := fmt.Sprintf("chunks/%02d/%05d", i%7, i)
		want = append(want, k)
		if err := put(t, b, k, []byte("x"), true); err != nil {
			t.Fatal(err)
		}
	}
	_ = put(t, b, "other/x", []byte("x"), true)
	var got []string
	if err := b.List(ctx, "chunks/", func(o datastore.ObjectInfo) error { got = append(got, o.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("listed %d keys, want %d", len(got), len(want))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] >= got[i] {
			t.Fatalf("not in order at %d: %q >= %q", i, got[i-1], got[i])
		}
	}
	// an error from the callback stops the listing and is returned
	stop := errors.New("stop")
	n := 0
	if err := b.List(ctx, "chunks/", func(datastore.ObjectInfo) error { n++; return stop }); !errors.Is(err, stop) || n != 1 {
		t.Fatalf("callback error: %v after %d calls", err, n)
	}
	// start-after
	pg, err := b.ListPage(ctx, "chunks/", "chunks/03/", "", 5)
	if err != nil || len(pg.Objects) != 5 || pg.Next == "" || pg.Objects[0].Key <= "chunks/03/" {
		t.Fatalf("page after start-after: %+v %v", pg, err)
	}
}

func TestSpecialKeys(t *testing.T) {
	srv, b := newFake(t)
	ctx := context.Background()
	keys := []string{"a b", "plus+sign", "percent%20", "eq=uals&amp", "q?uestion#hash", "tilde~dot.", "unicode/äöü/日本", "semi;colon,comma", "star*paren()", "quote'\"", "ÿþ"}
	keys = append(keys, strings.Repeat("long/", 100)+"end")
	for _, k := range keys {
		if err := put(t, b, k, []byte(k), true); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
		if got := mustGet(t, b, k); string(got) != k {
			t.Fatalf("get %q returned %q", k, got)
		}
	}
	var listed []string
	if err := b.List(ctx, "", func(o datastore.ObjectInfo) error { listed = append(listed, o.Key); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(listed) != len(keys) {
		t.Fatalf("listed %d, want %d: %q", len(listed), len(keys), listed)
	}
	if got := srv.Keys(testBucket); len(got) != len(keys) {
		t.Fatalf("the service holds %d keys", len(got))
	}
	for _, k := range []string{"", "/lead", "bad\x00nul", strings.Repeat("x", 1100)} {
		if err := put(t, b, k, []byte("x"), true); err == nil {
			t.Fatalf("key %q was accepted", k)
		}
	}
}

func TestEscapedPathMatchesSignedPath(t *testing.T) {
	for _, k := range []string{"a b", "x+y", "a%b", "k=v&w", "q?x#y", "日本", "a//b", "~._-", "colon:at@dollar$comma,semi;", "!*'()"} {
		raw := "/bucket/" + encodePath(k)
		u := buildURL("https", "h", raw, map[string]string{"prefix": k, "list-type": "2"})
		if u.EscapedPath() != raw {
			t.Errorf("%q: the transport would send %q, signed %q", k, u.EscapedPath(), raw)
		}
		dec, err := url.PathUnescape(strings.TrimPrefix(raw, "/bucket/"))
		if err != nil || dec != k {
			t.Errorf("%q does not decode back: %q %v", k, dec, err)
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || q.Get("prefix") != k {
			t.Errorf("%q: query decodes to %q (%v)", k, q.Get("prefix"), err)
		}
	}
}

func TestPrefix(t *testing.T) {
	srv, _ := newFake(t)
	cfg := testConfig(srv)
	cfg.Prefix = "/repos/main/"
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg2 := testConfig(srv)
	cfg2.Prefix = "repos/other"
	b2, _ := New(cfg2)
	_ = put(t, b, "chunks/a", []byte("1"), true)
	_ = put(t, b2, "chunks/a", []byte("2"), true)
	if got := srv.Keys(testBucket); len(got) != 2 || got[0] != "repos/main/chunks/a" || got[1] != "repos/other/chunks/a" {
		t.Fatalf("keys on the service: %q", got)
	}
	var keys []string
	_ = b.List(context.Background(), "", func(o datastore.ObjectInfo) error { keys = append(keys, o.Key); return nil })
	if len(keys) != 1 || keys[0] != "chunks/a" {
		t.Fatalf("listing through the prefix: %q", keys)
	}
	if got := mustGet(t, b2, "chunks/a"); string(got) != "2" {
		t.Fatalf("got %q", got)
	}
}

func TestSizeChecks(t *testing.T) {
	srv, b := newFake(t)
	ctx := context.Background()
	if err := b.Put(ctx, "short", bytes.NewReader([]byte("abc")), 10, datastore.PutOptions{}); !errors.Is(err, ErrSize) {
		t.Fatalf("short seekable reader: %v", err)
	}
	if err := b.Put(ctx, "long", bytes.NewReader([]byte("abcdef")), 3, datastore.PutOptions{}); !errors.Is(err, ErrSize) {
		t.Fatalf("long seekable reader: %v", err)
	}
	if err := b.Put(ctx, "short2", io.MultiReader(strings.NewReader("abc")), 10, datastore.PutOptions{}); !errors.Is(err, ErrSize) {
		t.Fatalf("short streaming reader: %v", err)
	}
	if err := b.Put(ctx, "long2", io.MultiReader(strings.NewReader("abcdef")), 3, datastore.PutOptions{}); !errors.Is(err, ErrSize) {
		t.Fatalf("long streaming reader: %v", err)
	}
	if err := b.Put(ctx, "ok", io.MultiReader(strings.NewReader("abc")), 3, datastore.PutOptions{}); err != nil {
		t.Fatal(err)
	}
	if keys := srv.Keys(testBucket); len(keys) != 1 || keys[0] != "ok" {
		t.Fatalf("a refused put left objects behind: %q", keys)
	}
	var fail failingReader
	if err := b.Put(ctx, "failing", &fail, 100, datastore.PutOptions{}); err == nil {
		t.Fatal("a reader that fails midway reported success")
	}
	if len(srv.Keys(testBucket)) != 1 {
		t.Fatal("a failed reader left an object")
	}
}

type failingReader struct{ n int }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.n > 0 {
		return 0, errors.New("disk on fire")
	}
	f.n++
	return copy(p, "partial"), nil
}

func TestRetriesOnSlowDown(t *testing.T) {
	srv, b := newFake(t)
	rule := srv.SlowDownBurst(3)
	if err := put(t, b, "k", []byte("v"), true); err != nil {
		t.Fatalf("put through a SlowDown burst: %v", err)
	}
	if rule.Hits() != 3 {
		t.Fatalf("the burst hit %d times", rule.Hits())
	}
	srv.ClearRules()
	// 500 on a read
	srv.FailNext("GetObject", 2, 500, "InternalError")
	if got := mustGet(t, b, "k"); string(got) != "v" {
		t.Fatal("get after 500s")
	}
	// exhausting the budget returns the error
	srv.ClearRules()
	srv.FailNext("GetObject", 100, 503, "SlowDown")
	srv.ResetLog()
	_, err := b.Get(context.Background(), "k")
	var e *Error
	if !errors.As(err, &e) || e.Code != "SlowDown" {
		t.Fatalf("exhausted retries: %v", err)
	}
	if n := srv.Count("GetObject"); n != 6 {
		t.Fatalf("made %d attempts, want 6", n)
	}
}

func TestNoRetryOnClientErrors(t *testing.T) {
	srv, b := newFake(t)
	srv.RemoveUser(srv.AccessKey())
	srv.ResetLog()
	_, err := b.Head(context.Background(), "k")
	var e *Error
	if !errors.As(err, &e) || e.Code != "InvalidAccessKeyId" || e.Status != 403 {
		t.Fatalf("revoked key: %v", err)
	}
	if srv.Count("") != 1 {
		t.Fatalf("a 403 was retried %d times", srv.Count(""))
	}
}

func TestMissingBucketIsNotNotFound(t *testing.T) {
	srv, _ := newFake(t)
	cfg := testConfig(srv)
	cfg.Bucket = "no-such-bucket"
	b, _ := New(cfg)
	ctx := context.Background()
	if _, err := b.Get(ctx, "k"); err == nil || errors.Is(err, datastore.ErrNotFound) || !IsCode(err, "NoSuchBucket") {
		t.Fatalf("get in a missing bucket: %v", err)
	}
	if _, err := b.Head(ctx, "k"); err == nil || errors.Is(err, datastore.ErrNotFound) {
		t.Fatalf("head in a missing bucket: %v", err)
	}
	if err := b.Delete(ctx, "k"); err == nil {
		t.Fatal("delete in a missing bucket succeeded")
	}
	if err := b.List(ctx, "", func(datastore.ObjectInfo) error { return nil }); err == nil {
		t.Fatal("list in a missing bucket succeeded")
	}
}

func TestLostResponseOnCreateOnlyPut(t *testing.T) {
	srv, b := newFake(t)
	srv.LoseNextResponse("PutObject")
	data := randBytes(100 << 10)
	if err := put(t, b, "lost", data, true); err != nil {
		t.Fatalf("a create-only put whose answer was lost must succeed: %v", err)
	}
	if got, _ := srv.Object(testBucket, "lost"); !bytes.Equal(got, data) {
		t.Fatal("stored bytes differ")
	}
	if n := srv.Count("PutObject"); n != 2 {
		t.Fatalf("%d put requests, want 2", n)
	}
	// If a different writer got the key first, the lost-response logic must not claim success.
	srv.ClearRules()
	srv.PutDirect(testBucket, "taken", []byte("someone else"), time.Now())
	srv.AddRule(s3fake.Rule{Op: "PutObject", Times: 1, Action: s3fake.Action{Status: 500, Code: "InternalError"}})
	if err := put(t, b, "taken", []byte("mine mine mine"), true); !errors.Is(err, datastore.ErrExists) {
		t.Fatalf("a conflict after a retry: %v", err)
	}
	// and a plain conflict without any retry is ErrExists without extra reads
	srv.ResetLog()
	if err := put(t, b, "taken", []byte("again"), true); !errors.Is(err, datastore.ErrExists) {
		t.Fatal(err)
	}
	if srv.Count("GetObject") != 0 || srv.Count("HeadObject") != 0 {
		t.Fatal("an unambiguous 412 triggered a comparison")
	}
}

func TestTornUploadIsRetriedAndLeavesNothing(t *testing.T) {
	srv, b := newFake(t)
	data := randBytes(2 << 20)
	srv.TearNextUpload(300 << 10)
	if err := put(t, b, "torn", data, true); err != nil {
		t.Fatalf("put after a torn upload: %v", err)
	}
	if got, _ := srv.Object(testBucket, "torn"); !bytes.Equal(got, data) {
		t.Fatal("stored bytes differ")
	}
	// with retries disabled the tear is an error and no object remains
	cfg := testConfig(srv)
	cfg.Attempts = 1
	b1, _ := New(cfg)
	srv.TearNextUpload(300 << 10)
	if err := b1.Put(context.Background(), "torn2", bytes.NewReader(data), int64(len(data)), datastore.PutOptions{IfAbsent: true}); err == nil {
		t.Fatal("a torn upload reported success")
	}
	if _, ok := srv.Object(testBucket, "torn2"); ok {
		t.Fatal("a torn upload left an object")
	}
	if err := put(t, b, "torn2", data, true); err != nil {
		t.Fatalf("the key must be free after a torn upload: %v", err)
	}
}

func TestStalledRequestIsAbortedAndRetried(t *testing.T) {
	srv, _ := newFake(t)
	cfg := testConfig(srv)
	cfg.StallTimeout = 150 * time.Millisecond
	b, _ := New(cfg)
	_ = put(t, b, "k", []byte("v"), true)
	srv.AddRule(s3fake.Rule{Op: "GetObject", Times: 1, Action: s3fake.Action{Hang: true}})
	start := time.Now()
	if got := mustGet(t, b, "k"); string(got) != "v" {
		t.Fatal("get after a hang")
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > 5*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestContextCancel(t *testing.T) {
	srv, b := newFake(t)
	srv.AddRule(s3fake.Rule{Op: "GetObject", Action: s3fake.Action{Hang: true}})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := b.Get(ctx, "k")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestBackoffWait(t *testing.T) {
	srv, _ := newFake(t)
	cfg := testConfig(srv)
	var waits []time.Duration
	cfg.Sleep = func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }
	cfg.BackoffBase, cfg.BackoffMax = 100*time.Millisecond, time.Second
	b, _ := New(cfg)
	srv.FailNext("HeadObject", 100, 503, "SlowDown")
	_, err := b.Head(context.Background(), "k")
	if err == nil || len(waits) != 5 {
		t.Fatalf("waits %v err %v", waits, err)
	}
	for i, w := range waits {
		ceil := min(100*time.Millisecond<<i, time.Second)
		if w <= 0 || w > ceil {
			t.Errorf("wait %d is %v, ceiling %v", i, w, ceil)
		}
	}
}

func TestParallelRequestsShareConnections(t *testing.T) {
	srv, b := newFake(t)
	var opened atomic.Int32
	srv2 := httptest.NewUnstartedServer(srv)
	srv2.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			opened.Add(1)
		}
	}
	srv2.Start()
	defer srv2.Close()
	cfg := testConfig(srv)
	cfg.Endpoint = srv2.Listener.Addr().String()
	b2, _ := New(cfg)
	_ = b
	for i := range 64 {
		_ = put(t, b2, fmt.Sprintf("p/%d", i), []byte("x"), true)
	}
	srv.SetLatency(5 * time.Millisecond)
	for round := range 3 {
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := b2.Head(context.Background(), fmt.Sprintf("p/%d", i)); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		_ = round
	}
	if srv.PeakInflight() < 8 {
		t.Fatalf("only %d requests were ever in flight", srv.PeakInflight())
	}
	// 64 sequential puts reuse one connection; 3 rounds of 32 parallel heads reuse the pool.
	if n := opened.Load(); n > 40 {
		t.Fatalf("%d connections were opened for 160 requests", n)
	}
}

func TestVirtualHostAddressing(t *testing.T) {
	srv, _ := newFake(t, s3fake.Options{Domain: "s3.test.invalid"})
	cfg := testConfig(srv)
	cfg.Endpoint = "s3.test.invalid:9000"
	cfg.Addressing = AddressingVirtual
	cfg.HTTPClient = &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, srv.Endpoint())
	}}}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, b, "v/k y", []byte("hello"), true); err != nil {
		t.Fatal(err)
	}
	if got := mustGet(t, b, "v/k y"); string(got) != "hello" {
		t.Fatal("virtual-host get")
	}
	var hosts []string
	for _, r := range srv.Requests() {
		hosts = append(hosts, r.Host)
		if r.Bucket != testBucket {
			t.Errorf("request %s addressed bucket %q", r.Op, r.Bucket)
		}
	}
	if len(hosts) == 0 || !strings.HasPrefix(hosts[0], testBucket+".s3.test.invalid") {
		t.Fatalf("hosts %v", hosts)
	}
	n := 0
	_ = b.List(context.Background(), "", func(datastore.ObjectInfo) error { n++; return nil })
	if n != 1 {
		t.Fatalf("listed %d", n)
	}
}

func TestPayloadSigning(t *testing.T) {
	srv, b := newFake(t)
	_ = put(t, b, "k", []byte("abc"), true)
	hash := srv.Requests()[0].Header.Get("X-Amz-Content-Sha256")
	if hash != sha256Hex([]byte("abc")) {
		t.Fatalf("plain HTTP must sign the payload, header is %q", hash)
	}
	cfg := testConfig(srv)
	cfg.Payload = PayloadNever
	b2, _ := New(cfg)
	srv.ResetLog()
	_ = put(t, b2, "k2", []byte("abc"), true)
	if h := srv.Requests()[0].Header.Get("X-Amz-Content-Sha256"); h != unsignedBody {
		t.Fatalf("PayloadNever sent %q", h)
	}
	// a wrong payload hash is refused by the service: flip a byte in flight
	cfg.Payload = PayloadAlways
	cfg.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPut {
			data, _ := io.ReadAll(r.Body)
			data[0] ^= 1
			r.Body = io.NopCloser(bytes.NewReader(data))
		}
		return http.DefaultTransport.RoundTrip(r)
	})}
	b3, _ := New(cfg)
	if err := put(t, b3, "k3", []byte("abc"), true); !IsCode(err, "XAmzContentSHA256Mismatch") {
		t.Fatalf("tampered body: %v", err)
	}
	if _, ok := srv.Object(testBucket, "k3"); ok {
		t.Fatal("a tampered body was stored")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTLSUsesUnsignedPayloadOverHTTP2(t *testing.T) {
	srv, _ := newFake(t)
	ts := httptest.NewUnstartedServer(srv)
	ts.EnableHTTP2 = true
	ts.Config.ErrorLog = log.New(io.Discard, "", 0)
	ts.StartTLS()
	defer ts.Close()
	cfg := testConfig(srv)
	cfg.NoTLS = false
	cfg.Endpoint = "https://" + ts.Listener.Addr().String()
	cfg.InsecureSkipVerify = true
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := put(t, b, "tls", randBytes(1<<20), true); err != nil {
		t.Fatal(err)
	}
	reqs := srv.Requests()
	if h := reqs[len(reqs)-1].Header.Get("X-Amz-Content-Sha256"); h != unsignedBody {
		t.Fatalf("TLS must leave the payload unsigned, header is %q", h)
	}
	if len(mustGet(t, b, "tls")) != 1<<20 {
		t.Fatal("tls get")
	}
	// a pinned private CA works without InsecureSkipVerify
	cfg.InsecureSkipVerify = false
	cfg.CAPEM = string(pemOf(ts))
	b2, _ := New(cfg)
	if _, err := b2.Head(context.Background(), "tls"); err != nil {
		t.Fatalf("with the CA: %v", err)
	}
	cfg.CAPEM = ""
	b3, _ := New(cfg)
	if _, err := b3.Head(context.Background(), "tls"); err == nil {
		t.Fatal("an unknown CA was trusted")
	}
}

func TestCredentialsNeverPrinted(t *testing.T) {
	srv, b := newFake(t)
	secret := srv.SecretKey()
	cfg := testConfig(srv)
	cfg.Credentials.SessionToken = "tok-secret-token"
	for _, s := range []string{fmt.Sprint(cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg), fmt.Sprintf("%v", cfg.Credentials),
		fmt.Sprintf("%#v", cfg.Credentials), cfg.String(), b.Describe()} {
		if strings.Contains(s, secret) || strings.Contains(s, "tok-secret-token") {
			t.Fatalf("a printed form leaks the secret: %s", s)
		}
	}
	j, _ := cfg.Credentials.MarshalJSON()
	if strings.Contains(string(j), secret) {
		t.Fatal("json leaks the secret")
	}
	// errors from a failing service carry no secret and the service never sees it
	srv.FailNext("", 100, 403, "AccessDenied")
	_, err := b.Head(context.Background(), "k")
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error text: %v", err)
	}
	for _, r := range srv.Requests() {
		for k, v := range r.Header {
			if strings.Contains(strings.Join(v, ","), secret) {
				t.Fatalf("header %s carries the secret", k)
			}
		}
	}
}

func TestProbe(t *testing.T) {
	srv, b := newFake(t)
	rs, err := b.Probe(context.Background())
	if err != nil {
		t.Fatalf("probe of a good service: %v %+v", err, rs)
	}
	if len(rs) < 9 {
		t.Fatalf("only %d steps", len(rs))
	}
	if keys := srv.Keys(testBucket); len(keys) != 0 {
		t.Fatalf("the probe left %q", keys)
	}
	// a service that overwrites despite the condition is refused
	srv.SetQuirk(s3fake.QuirkIgnoreIfNoneMatch, true)
	rs, err = b.Probe(context.Background())
	if err == nil {
		t.Fatal("probe accepted a service that overwrites")
	}
	found := false
	for _, r := range rs {
		if !r.OK && strings.Contains(r.Err, "overwrote") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failure is not named: %+v", rs)
	}
	srv.SetQuirk(s3fake.QuirkIgnoreIfNoneMatch, false)
	// a missing bucket is reported at the first step
	cfg := testConfig(srv)
	cfg.Bucket = "absent-bucket"
	b2, _ := New(cfg)
	rs, err = b2.Probe(context.Background())
	if err == nil || len(rs) != 1 || rs[0].OK {
		t.Fatalf("probe of a missing bucket: %v %+v", err, rs)
	}
}

func TestListRejectsMisbehavingService(t *testing.T) {
	for _, q := range []s3fake.Quirk{s3fake.QuirkUnsortedListing, s3fake.QuirkRepeatPageEdge, s3fake.QuirkNoContinuation} {
		srv, b := newFake(t)
		want := 1100
		for i := range want {
			srv.PutDirect(testBucket, fmt.Sprintf("k/%04d", i), []byte("x"), time.Now())
		}
		srv.SetQuirk(q, true)
		n := 0
		err := b.List(context.Background(), "k/", func(datastore.ObjectInfo) error { n++; return nil })
		if q == s3fake.QuirkNoContinuation {
			// a service that cuts the listing at one page without saying so cannot be detected by the client;
			// the conformance suite catches it. Here it must at least not loop or claim more keys than exist.
			if err != nil || n > want {
				t.Errorf("%s: %d keys, %v", q, n, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: the listing was accepted (%d keys)", q, n)
		}
	}
}

func TestUnsupportedFeaturesAreRefusedByTheFake(t *testing.T) {
	srv, b := newFake(t)
	// every feature the client does not use answers 501, so a code change that starts using one fails loudly
	for _, c := range []struct{ method, query string }{
		{"POST", "uploads="}, {"GET", "versioning="}, {"GET", "acl="}, {"GET", "tagging="}, {"GET", "lifecycle="}, {"GET", "uploads="}, {"GET", "delimiter=%2F&list-type=2"},
	} {
		path := "/" + testBucket + "/k"
		if strings.HasPrefix(c.query, "delimiter") {
			path = "/" + testBucket
		}
		req, _ := http.NewRequest(c.method, "http://"+srv.Endpoint()+path+"?"+c.query, nil)
		signV4(req, b.cfg.Credentials, srv.Region(), emptyPayload, time.Now())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 501 {
			t.Errorf("%s ?%s answered %d, want 501", c.method, c.query, resp.StatusCode)
		}
	}
}

// The shared backend contract of the repository, run against the S3 client and the fake.
func TestDatastoreConformance(t *testing.T) {
	srv, err := s3fake.New(s3fake.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	_ = srv.EnsureBucket(testBucket)
	var n atomic.Int32
	datastore.RunConformance(t, func(t *testing.T) datastore.Backend {
		cfg := testConfig(srv)
		cfg.Bucket = fmt.Sprintf("conf-%d", n.Add(1))
		_ = srv.EnsureBucket(cfg.Bucket)
		b, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(b.Close)
		return b
	})
}
