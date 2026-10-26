package conformance

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/s3"
)

// Checks returns the whole suite in running order.
func Checks() []Check {
	return []Check{
		{"probe/passes", checkProbe},
		{"bucket/head", checkBucketHead},
		{"bucket/missing-is-a-hard-error", checkMissingBucket},
		{"object/put-get-roundtrip-sizes", checkRoundtrip},
		{"object/empty", checkEmptyObject},
		{"object/head-size-and-time", checkHead},
		{"object/modtime-is-stable", checkModTimeStable},
		{"object/head-missing", checkHeadMissing},
		{"object/get-missing", checkGetMissing},
		{"object/overwrite-unconditional", checkOverwrite},
		{"object/range-middle", checkRangeMiddle},
		{"object/range-open-ended", checkRangeOpen},
		{"object/range-clamped-at-end", checkRangeClamped},
		{"object/range-of-missing", checkRangeMissing},
		{"object/delete-is-idempotent", checkDeleteIdempotent},
		{"object/delete-removes", checkDeleteRemoves},
		{"object/special-character-keys", checkSpecialKeys},
		{"object/long-keys", checkLongKeys},
		{"object/large", checkLarge},
		{"put/if-none-match-refuses-existing", checkIfAbsentRefuses},
		{"put/if-none-match-keeps-the-original", checkIfAbsentKeeps},
		{"put/if-none-match-is-atomic-under-concurrency", checkIfAbsentRace},
		{"put/if-none-match-distinct-keys-all-succeed", checkIfAbsentDistinct},
		{"put/if-none-match-after-delete", checkIfAbsentAfterDelete},
		{"put/if-none-match-empty-object-exists", checkIfAbsentEmpty},
		{"put/torn-upload-leaves-no-object", checkTornUpload},
		{"put/size-mismatch-refused", checkSizeMismatch},
		{"put/read-your-write", checkReadYourWrite},
		{"put/parallel-puts-and-gets", checkParallel},
		{"list/empty", checkListEmpty},
		{"list/prefix", checkListPrefix},
		{"list/prefix-is-not-component-aligned", checkListPrefixPartial},
		{"list/lexical-order", checkListOrder},
		{"list/over-1000-keys", checkListMany},
		{"list/pagination-with-continuation-tokens", checkListPagination},
		{"list/start-after", checkListStartAfter},
		{"list/special-characters", checkListSpecial},
		{"list/deleted-keys-disappear", checkListAfterDelete},
		{"list/info-size-and-time", checkListInfo},
		{"list/callback-error-stops", checkListStop},
		{"auth/wrong-secret-refused", checkWrongSecret},
		{"auth/unknown-access-key-refused", checkUnknownKey},
		{"auth/anonymous-refused", checkAnonymous},
		{"auth/tampered-request-refused", checkTampered},
		{"auth/skewed-clock-refused", checkSkew},
	}
}

func checkProbe(c *C) {
	rs, err := c.B.Probe(c.Ctx)
	if err != nil {
		c.Fatalf("probe: %v %+v", err, rs)
	}
	if keys := c.list(""); len(keys) != 0 {
		c.Fatalf("the probe left %v", keys)
	}
}

func checkBucketHead(c *C) { c.must(c.B.HeadBucket(c.Ctx), "head bucket") }

func checkMissingBucket(c *C) {
	b, err := newBackend(c.Target, c.Bucket+"-absent", "")
	c.must(err, "backend")
	if _, err := b.Get(c.Ctx, "k"); err == nil || errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("get in a missing bucket: %v (must be a hard error, not not-found)", err)
	}
	if _, err := b.Head(c.Ctx, "k"); err == nil || errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("head in a missing bucket: %v", err)
	}
	if err := b.List(c.Ctx, "", func(datastore.ObjectInfo) error { return nil }); err == nil {
		c.Fatalf("list in a missing bucket succeeded")
	}
	if err := b.HeadBucket(c.Ctx); err == nil {
		c.Fatalf("head of a missing bucket succeeded")
	}
}

func checkRoundtrip(c *C) {
	for i, n := range []int{1, 100, 4095, 64 << 10, 1 << 20, 5<<20 + 7} {
		d := c.data(n)
		k := fmt.Sprintf("rt/%d", i)
		c.mustPut(k, d, true)
		if got := c.mustGet(k); !bytes.Equal(got, d) {
			c.Fatalf("%d bytes: content differs after a round trip", n)
		}
	}
}

func checkEmptyObject(c *C) {
	c.mustPut("empty", nil, true)
	if got := c.mustGet("empty"); len(got) != 0 {
		c.Fatalf("empty object holds %d bytes", len(got))
	}
	info, err := c.B.Head(c.Ctx, "empty")
	c.must(err, "head")
	if info.Size != 0 {
		c.Fatalf("size %d", info.Size)
	}
	if got, err := c.rng("empty", 0, -1); err != nil || len(got) != 0 {
		c.Fatalf("range of the empty object: %d bytes, %v", len(got), err)
	}
}

func checkHead(c *C) {
	d := c.data(12345)
	c.mustPut("h", d, true)
	info, err := c.B.Head(c.Ctx, "h")
	c.must(err, "head")
	if info.Size != int64(len(d)) || info.Key != "h" {
		c.Fatalf("head: %+v", info)
	}
	if dt := time.Since(info.ModTime); dt < -5*time.Minute || dt > 10*time.Minute {
		c.Fatalf("the object was just written but its time is %v (%v ago): garbage collection ages objects by it", info.ModTime, dt)
	}
}

func checkModTimeStable(c *C) {
	c.mustPut("m", c.data(100), true)
	a, err := c.B.Head(c.Ctx, "m")
	c.must(err, "head")
	time.Sleep(1200 * time.Millisecond)
	b, err := c.B.Head(c.Ctx, "m")
	c.must(err, "head")
	if !a.ModTime.Equal(b.ModTime) {
		c.Fatalf("the write time changed between two reads: %v then %v", a.ModTime, b.ModTime)
	}
	var l datastore.ObjectInfo
	c.must(c.B.List(c.Ctx, "m", func(o datastore.ObjectInfo) error { l = o; return nil }), "list")
	if dt := l.ModTime.Sub(a.ModTime); dt < -2*time.Second || dt > 2*time.Second {
		c.Fatalf("list says %v, head says %v", l.ModTime, a.ModTime)
	}
}

func checkHeadMissing(c *C) {
	if _, err := c.B.Head(c.Ctx, "no/such"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("head of a missing key: %v", err)
	}
}

func checkGetMissing(c *C) {
	if _, err := c.B.Get(c.Ctx, "no/such"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("get of a missing key: %v", err)
	}
	c.mustPut("obj", []byte("x"), true)
	if _, err := c.B.Get(c.Ctx, "obj/child"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("get below an object: %v", err)
	}
}

func checkOverwrite(c *C) {
	c.mustPut("o", []byte("one"), false)
	c.mustPut("o", []byte("two!"), false)
	if got := c.mustGet("o"); string(got) != "two!" {
		c.Fatalf("an unconditional put left %q", got)
	}
}

func checkRangeMiddle(c *C) {
	d := c.data(100000)
	c.mustPut("r", d, true)
	for _, r := range [][2]int64{{0, 1}, {5, 10}, {50000, 20000}, {99999, 1}} {
		got, err := c.rng("r", r[0], r[1])
		c.must(err, fmt.Sprintf("range %v", r))
		if !bytes.Equal(got, d[r[0]:r[0]+r[1]]) {
			c.Fatalf("range %v returned %d bytes with other content", r, len(got))
		}
	}
}

func checkRangeOpen(c *C) {
	d := c.data(5000)
	c.mustPut("r", d, true)
	got, err := c.rng("r", 1234, -1)
	c.must(err, "open range")
	if !bytes.Equal(got, d[1234:]) {
		c.Fatalf("open range returned %d bytes", len(got))
	}
	got, err = c.rng("r", 0, -1)
	c.must(err, "whole range")
	if !bytes.Equal(got, d) {
		c.Fatalf("range from 0 returned %d bytes", len(got))
	}
}

func checkRangeClamped(c *C) {
	d := c.data(1000)
	c.mustPut("r", d, true)
	got, err := c.rng("r", 990, 500)
	c.must(err, "range past the end")
	if !bytes.Equal(got, d[990:]) {
		c.Fatalf("a range running past the end returned %d bytes, want 10", len(got))
	}
	if got, err := c.rng("r", 1000, 10); err == nil && len(got) != 0 {
		c.Fatalf("a range at the end returned %d bytes", len(got))
	}
}

func checkRangeMissing(c *C) {
	if _, err := c.B.GetRange(c.Ctx, "no/such", 0, 10); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("range of a missing key: %v", err)
	}
}

func checkDeleteIdempotent(c *C) {
	c.must(c.B.Delete(c.Ctx, "never"), "delete of a missing key")
	c.mustPut("d", []byte("x"), true)
	for i := range 3 {
		c.must(c.B.Delete(c.Ctx, "d"), fmt.Sprintf("delete #%d", i))
	}
}

func checkDeleteRemoves(c *C) {
	c.mustPut("d1", []byte("1"), true)
	c.mustPut("d10", []byte("10"), true)
	c.must(c.B.Delete(c.Ctx, "d1"), "delete")
	if _, err := c.B.Head(c.Ctx, "d1"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("head after delete: %v", err)
	}
	if _, err := c.get("d1"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("get after delete: %v", err)
	}
	if got := c.mustGet("d10"); string(got) != "10" {
		c.Fatalf("deleting d1 touched d10")
	}
}

var awkward = []string{"a b", "plus+sign", "percent%20", "eq=sign", "amp&ersand", "question?mark", "hash#tag", "star*", "quote'single", "quote\"double",
	"colon:name", "at@sign", "dollar$", "paren(1)", "brace{x}", "bracket[0]", "semi;colon", "comma,comma", "tilde~", ".hidden", "..dots", "dots..",
	"ünïcödé/日本語/файл", "emoji-\U0001F600", "UPPER/Case", "upper/case", "-dash", "w-123"}

func checkSpecialKeys(c *C) {
	for i, k := range awkward {
		c.mustPut("sp/"+k, []byte(fmt.Sprintf("payload %d", i)), true)
	}
	for i, k := range awkward {
		if got := c.mustGet("sp/" + k); string(got) != fmt.Sprintf("payload %d", i) {
			c.Fatalf("key %q: wrong content %q", k, got)
		}
		info, err := c.B.Head(c.Ctx, "sp/"+k)
		if err != nil || info.Key != "sp/"+k {
			c.Fatalf("head %q: %+v %v", k, info, err)
		}
	}
}

func checkLongKeys(c *C) {
	k := "long/" + strings.Repeat("abcdefghij", 20) + "/" + strings.Repeat("klmnopqrst", 20) + "/" + strings.Repeat("uvwxyzabcd", 20) + "/" + strings.Repeat("z", 100)
	c.mustPut(k, []byte("long"), true)
	if got := c.mustGet(k); string(got) != "long" {
		c.Fatalf("long key round trip: %q", got)
	}
	if keys := c.list("long/"); len(keys) != 1 || keys[0] != k {
		c.Fatalf("long key listing: %v", head(keys))
	}
}

func checkLarge(c *C) {
	d := c.data(24 << 20)
	c.mustPut("large", d, true)
	rc, err := c.B.Get(c.Ctx, "large")
	c.must(err, "get")
	defer rc.Close()
	got, err := io.ReadAll(rc)
	c.must(err, "read")
	if !bytes.Equal(got, d) {
		c.Fatalf("24 MiB object differs")
	}
}

func checkIfAbsentRefuses(c *C) {
	c.mustPut("once", []byte("first"), true)
	err := c.put("once", []byte("second"), true)
	if !errors.Is(err, datastore.ErrExists) {
		c.Fatalf("second create-only put: %v, want ErrExists (a service that ignores If-None-Match overwrites)", err)
	}
}

func checkIfAbsentKeeps(c *C) {
	c.mustPut("once", []byte("first"), true)
	_ = c.put("once", []byte("second"), true)
	if got := c.mustGet("once"); string(got) != "first" {
		c.Fatalf("a refused create-only put changed the object to %q", got)
	}
}

func checkIfAbsentRace(c *C) {
	for round := range 4 {
		key := fmt.Sprintf("race/%d", round)
		const n = 16
		var wins, exists atomic.Int32
		winner := make([]atomic.Bool, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := c.put(key, []byte(fmt.Sprintf("writer-%02d-%s", i, strings.Repeat("x", 4000+i))), true)
				switch {
				case err == nil:
					wins.Add(1)
					winner[i].Store(true)
				case errors.Is(err, datastore.ErrExists):
					exists.Add(1)
				default:
					c.Errorf("racer %d: %v", i, err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 {
			c.Fatalf("round %d: %d writers succeeded with If-None-Match: *, exactly 1 must", round, wins.Load())
		}
		got := string(c.mustGet(key))
		for i := range n {
			if winner[i].Load() && !strings.HasPrefix(got, fmt.Sprintf("writer-%02d-", i)) {
				c.Fatalf("round %d: writer %d won but the object holds %.20q", round, i, got)
			}
		}
	}
}

func checkIfAbsentDistinct(c *C) {
	c.must(parallel(64, 16, func(i int) error { return c.put(fmt.Sprintf("distinct/%03d", i), []byte("v"), true) }), "parallel create-only puts of distinct keys")
}

func checkIfAbsentAfterDelete(c *C) {
	c.mustPut("k", []byte("a"), true)
	c.must(c.B.Delete(c.Ctx, "k"), "delete")
	c.mustPut("k", []byte("b"), true)
	if got := c.mustGet("k"); string(got) != "b" {
		c.Fatalf("got %q", got)
	}
}

func checkIfAbsentEmpty(c *C) {
	c.mustPut("e", nil, true)
	if err := c.put("e", []byte("x"), true); !errors.Is(err, datastore.ErrExists) {
		c.Fatalf("an empty existing object must count as existing: %v", err)
	}
}

type tearReader struct {
	left int
}

func (t *tearReader) Read(p []byte) (int, error) {
	if t.left <= 0 {
		return 0, errors.New("conformance: simulated client failure")
	}
	if len(p) > t.left {
		p = p[:t.left]
	}
	for i := range p {
		p[i] = byte(i)
	}
	t.left -= len(p)
	return len(p), nil
}

func checkTornUpload(c *C) {
	const size = 3 << 20
	req := c.rawReq("PUT", "torn", nil, &tearReader{left: 200 << 10}, size, "UNSIGNED-PAYLOAD", time.Now())
	req.Header.Set("If-None-Match", "*")
	// If-None-Match is signed (it is added after signing here, which services accept for unsigned headers)
	resp, err := c.client().Do(req)
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode < 400 {
			c.Fatalf("an upload whose body failed midway was answered %d", resp.StatusCode)
		}
	}
	deadline := time.Now().Add(1500 * time.Millisecond)
	for {
		_, herr := c.B.Head(c.Ctx, "torn")
		if herr == nil {
			c.Fatalf("a torn upload left an object behind")
		}
		if !errors.Is(herr, datastore.ErrNotFound) {
			c.Fatalf("head after a torn upload: %v", herr)
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	c.mustPut("torn", []byte("whole"), true)
}

func checkSizeMismatch(c *C) {
	if err := c.B.Put(c.Ctx, "short", bytes.NewReader([]byte("abc")), 10, datastore.PutOptions{}); err == nil {
		c.Fatalf("a short body was accepted")
	}
	if err := c.B.Put(c.Ctx, "long", bytes.NewReader([]byte("abcdef")), 3, datastore.PutOptions{}); err == nil {
		c.Fatalf("a long body was accepted")
	}
	if keys := c.list(""); len(keys) != 0 {
		c.Fatalf("a refused put left %v", keys)
	}
}

func checkReadYourWrite(c *C) {
	for i := range 30 {
		k := fmt.Sprintf("ryw/%03d", i)
		c.mustPut(k, []byte("v"), true)
		if _, err := c.B.Head(c.Ctx, k); err != nil {
			c.Fatalf("head right after put: %v", err)
		}
		found := false
		for _, l := range c.list(k) {
			found = found || l == k
		}
		if !found {
			c.Fatalf("%s is not listed right after its put", k)
		}
	}
}

func checkParallel(c *C) {
	data := make([][]byte, 48)
	for i := range data {
		data[i] = c.data(10000 + i*977)
	}
	c.must(parallel(len(data), 16, func(i int) error { return c.put(fmt.Sprintf("par/%03d", i), data[i], true) }), "parallel puts")
	c.must(parallel(len(data), 16, func(i int) error {
		got, err := c.get(fmt.Sprintf("par/%03d", i))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, data[i]) {
			return fmt.Errorf("object %d differs", i)
		}
		return nil
	}), "parallel gets")
}

func checkListEmpty(c *C) {
	if keys := c.list(""); len(keys) != 0 {
		c.Fatalf("a fresh listing shows %v", keys)
	}
	if keys := c.list("nothing/"); len(keys) != 0 {
		c.Fatalf("%v", keys)
	}
}

func populate(c *C, keys []string) {
	c.must(parallel(len(keys), 16, func(i int) error { return c.put(keys[i], []byte("v"), true) }), "populate")
}

func checkListPrefix(c *C) {
	keys := []string{"chunks/aa/bb/1", "chunks/aa/bb/2", "chunks/aa/cc/3", "chunks/ab/00/4", "groups/g1/s1/index", "groups/g1/s1/manifest", "groups/g2/s1/index", "datastore.json"}
	populate(c, keys)
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	c.equalStrings("list all", c.list(""), sorted)
	for _, p := range []string{"chunks/", "chunks/aa/", "chunks/aa/bb/", "groups/g1/", "groups/", "data", "nothing"} {
		var want []string
		for _, k := range sorted {
			if strings.HasPrefix(k, p) {
				want = append(want, k)
			}
		}
		c.equalStrings("list "+p, c.list(p), want)
	}
}

func checkListPrefixPartial(c *C) {
	populate(c, []string{"ab", "abc", "abc/d", "abd", "b"})
	c.equalStrings("list ab", c.list("ab"), []string{"ab", "abc", "abc/d", "abd"})
	c.equalStrings("list abc", c.list("abc"), []string{"abc", "abc/d"})
	c.equalStrings("list abc/", c.list("abc/"), []string{"abc/d"})
}

func checkListOrder(c *C) {
	keys := []string{"a-b", "a/b", "a.b", "a0", "a b", "Z", "a_b", "a~b", "é", "ü/x", "a+b", "a%b"}
	populate(c, keys)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	c.equalStrings("order", c.list(""), want)
}

func checkListMany(c *C) {
	const n = 2300
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("many/%02x/%05d", i%251, i)
	}
	populate(c, keys)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	got := c.list("many/")
	if len(got) != n {
		c.Fatalf("listed %d of %d keys", len(got), n)
	}
	c.equalStrings("many", got, want)
}

func checkListPagination(c *C) {
	const n = 45
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("pg/%03d", i)
	}
	populate(c, keys)
	var got []string
	token, pages := "", 0
	for {
		pg, err := c.B.ListPage(c.Ctx, "pg/", "", token, 7)
		c.must(err, "list page")
		pages++
		if len(pg.Objects) > 7 {
			c.Fatalf("a page of max-keys 7 holds %d keys", len(pg.Objects))
		}
		for _, o := range pg.Objects {
			got = append(got, o.Key)
		}
		if pg.Next == "" {
			break
		}
		if pages > 20 {
			c.Fatalf("pagination does not end")
		}
		token = pg.Next
	}
	if pages != 7 {
		c.Fatalf("%d pages for %d keys at 7 per page, want 7", pages, n)
	}
	c.equalStrings("paginated", got, keys)
}

func checkListStartAfter(c *C) {
	keys := []string{"sa/a", "sa/b", "sa/c", "sa/d", "sa/e"}
	populate(c, keys)
	var got []string
	c.must(c.B.ListFrom(c.Ctx, "sa/", "sa/b", func(o datastore.ObjectInfo) error { got = append(got, o.Key); return nil }), "list from")
	c.equalStrings("start-after", got, []string{"sa/c", "sa/d", "sa/e"})
	got = nil
	c.must(c.B.ListFrom(c.Ctx, "sa/", "sa/e", func(o datastore.ObjectInfo) error { got = append(got, o.Key); return nil }), "list from the last")
	if len(got) != 0 {
		c.Fatalf("after the last key: %v", got)
	}
}

func checkListSpecial(c *C) {
	var keys []string
	for _, k := range awkward {
		keys = append(keys, "ls/"+k)
	}
	populate(c, keys)
	want := append([]string(nil), keys...)
	sort.Strings(want)
	c.equalStrings("special", c.list("ls/"), want)
	c.equalStrings("plus prefix", c.list("ls/plus+"), []string{"ls/plus+sign"})
	c.equalStrings("percent prefix", c.list("ls/percent%"), []string{"ls/percent%20"})
}

func checkListAfterDelete(c *C) {
	populate(c, []string{"ad/1", "ad/2", "ad/3"})
	c.must(c.B.Delete(c.Ctx, "ad/2"), "delete")
	c.equalStrings("after delete", c.list("ad/"), []string{"ad/1", "ad/3"})
}

func checkListInfo(c *C) {
	c.mustPut("li/a", c.data(1234), true)
	var o datastore.ObjectInfo
	c.must(c.B.List(c.Ctx, "li/", func(x datastore.ObjectInfo) error { o = x; return nil }), "list")
	if o.Key != "li/a" || o.Size != 1234 || o.ModTime.IsZero() || time.Since(o.ModTime) > 10*time.Minute || time.Until(o.ModTime) > 5*time.Minute {
		c.Fatalf("list info %+v", o)
	}
}

func checkListStop(c *C) {
	populate(c, []string{"st/1", "st/2", "st/3", "st/4", "st/5"})
	stop := errors.New("enough")
	n := 0
	err := c.B.List(c.Ctx, "st/", func(datastore.ObjectInfo) error {
		n++
		if n == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 2 {
		c.Fatalf("list stopped with %v after %d calls", err, n)
	}
}

// ---- authentication ------------------------------------------------------------

func (c *C) authStatus(req *http.Request) int {
	resp, err := c.client().Do(req)
	c.must(err, "raw request")
	resp.Body.Close()
	return resp.StatusCode
}

func (c *C) seedObject() {
	c.mustPut("auth/obj", []byte("secret content"), true)
}

func checkWrongSecret(c *C) {
	c.seedObject()
	req := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now())
	req.Header.Del("Authorization")
	s3.SignRequest(req, s3.Credentials{AccessKey: c.Target.AccessKey, SecretKey: c.Target.SecretKey + "x"}, c.Target.Region, "", time.Now())
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("a request signed with the wrong secret got HTTP %d, want 403", st)
	}
}

func checkUnknownKey(c *C) {
	c.seedObject()
	req := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now())
	req.Header.Del("Authorization")
	s3.SignRequest(req, s3.Credentials{AccessKey: "NOSUCHKEY" + c.Target.AccessKey, SecretKey: c.Target.SecretKey}, c.Target.Region, "", time.Now())
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("an unknown access key got HTTP %d", st)
	}
}

func checkAnonymous(c *C) {
	c.seedObject()
	req := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now())
	for _, h := range []string{"Authorization", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		req.Header.Del(h)
	}
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("an anonymous read got HTTP %d", st)
	}
	req = c.rawReq("PUT", "auth/anon", nil, strings.NewReader("x"), 1, "UNSIGNED-PAYLOAD", time.Now())
	for _, h := range []string{"Authorization", "X-Amz-Date", "X-Amz-Content-Sha256"} {
		req.Header.Del(h)
	}
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("an anonymous write got HTTP %d", st)
	}
	if _, err := c.B.Head(c.Ctx, "auth/anon"); !errors.Is(err, datastore.ErrNotFound) {
		c.Fatalf("an anonymous write created an object: %v", err)
	}
}

func checkTampered(c *C) {
	c.seedObject()
	c.mustPut("auth/other", []byte("other"), true)
	// sign a request for one key, send it for another
	req := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now())
	other := c.rawReq("GET", "auth/other", nil, nil, 0, "", time.Now())
	req.URL = other.URL
	req.Host = other.Host
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("a request redirected to another key after signing got HTTP %d", st)
	}
	// and a signed GET turned into a DELETE
	del := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now())
	del.Method = http.MethodDelete
	if st := c.authStatus(del); st < 400 || st >= 500 {
		c.Fatalf("a signed GET replayed as DELETE got HTTP %d", st)
	}
	if _, err := c.B.Head(c.Ctx, "auth/obj"); err != nil {
		c.Fatalf("the replayed DELETE removed the object: %v", err)
	}
}

func checkSkew(c *C) {
	c.seedObject()
	req := c.rawReq("GET", "auth/obj", nil, nil, 0, "", time.Now().Add(-2*time.Hour))
	if st := c.authStatus(req); st < 400 || st >= 500 {
		c.Fatalf("a request signed two hours in the past got HTTP %d", st)
	}
}
