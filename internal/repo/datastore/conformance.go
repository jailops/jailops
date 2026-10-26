package datastore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RunConformance runs the backend contract against implementations produced
// by factory. It is exported so that every backend (memory, directory, S3)
// proves the same behaviour with the same test.
//
// factory is called once per subtest and must return an EMPTY backend (a
// fresh directory, a fresh prefix); it may register cleanup with t.Cleanup.
// The suite calls t.Fatal on a violation; it never skips.
//
// What is checked: exact-size puts, create-only atomicity under 16 concurrent
// writers, no object left behind by a torn put and an existing object left
// intact by a torn replacement, listing by prefix in lexical order exactly once
// including 2500 keys, range reads, idempotent delete, awkward keys, rejection
// of keys that could escape the repository, and context cancellation.
func RunConformance(t *testing.T, factory func(t *testing.T) Backend) {
	t.Helper()
	for _, c := range conformanceCases() {
		t.Run(c.name, func(t *testing.T) {
			b := factory(t)
			if b == nil {
				t.Fatal("factory returned a nil backend")
			}
			c.fn(t, b)
		})
	}
}

// T is the part of testing.T the checks use. It lets the suite be run against
// a deliberately broken backend and be expected to fail (mutation tests).
type T interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

type conformanceCase struct {
	name string
	fn   func(t T, b Backend)
}

func conformanceCases() []conformanceCase {
	return []conformanceCase{
		{"Kind", confKind},
		{"PutGetHead", confPutGetHead},
		{"PutSizeMustBeExact", confSizeExact},
		{"CreateOnly", confCreateOnly},
		{"ConcurrentCreateOnly16", confConcurrentCreateOnly},
		{"TornPutLeavesNothing", confTornPut},
		{"TornReplaceKeepsOldObject", confTornReplace},
		{"NotFound", confNotFound},
		{"RangeReads", confRange},
		{"DeleteIsIdempotent", confDelete},
		{"ListPrefixAndOrder", confListOrder},
		{"ListStopsOnCallbackError", confListStop},
		{"ListLarge2500", confListLarge},
		{"AwkwardKeys", confAwkwardKeys},
		{"InvalidKeysRejected", confInvalidKeys},
		{"ReadYourWrites", confReadYourWrites},
		{"ContextCancelled", confContext},
		{"LargeObjectStreams", confLarge},
	}
}

func cbg() context.Context { return context.Background() }

func payload(seed int64, n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func put(t T, b Backend, key string, data []byte, o PutOptions) error {
	t.Helper()
	return b.Put(cbg(), key, bytes.NewReader(data), int64(len(data)), o)
}

func mustPut(t T, b Backend, key string, data []byte, o PutOptions) {
	t.Helper()
	if err := put(t, b, key, data, o); err != nil {
		t.Fatalf("put %q: %v", key, err)
	}
}

func get(t T, b Backend, key string) []byte {
	t.Helper()
	rc, err := b.Get(cbg(), key)
	if err != nil {
		t.Fatalf("get %q: %v", key, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading %q: %v", key, err)
	}
	return data
}

func mustMissing(t T, b Backend, key string) {
	t.Helper()
	if _, err := b.Head(cbg(), key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("head %q: want ErrNotFound, got %v", key, err)
	}
}

func listKeys(t T, b Backend, prefix string) []string {
	t.Helper()
	var keys []string
	if err := b.List(cbg(), prefix, func(o ObjectInfo) error { keys = append(keys, o.Key); return nil }); err != nil {
		t.Fatalf("list %q: %v", prefix, err)
	}
	return keys
}

func confKind(t T, b Backend) {
	switch b.Kind() {
	case "memory", "directory", "s3":
	default:
		t.Fatalf("Kind() = %q, want memory, directory or s3", b.Kind())
	}
}

func confPutGetHead(t T, b Backend) {
	start := time.Now().Add(-time.Hour)
	for i, n := range []int{0, 1, 4095, 65536, 5 << 20} {
		key := fmt.Sprintf("objects/o%d", i)
		data := payload(int64(i), n)
		mustPut(t, b, key, data, PutOptions{IfAbsent: true})
		if got := get(t, b, key); !bytes.Equal(got, data) {
			t.Fatalf("%s: content differs after round trip", key)
		}
		info, err := b.Head(cbg(), key)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size != int64(n) || info.Key != key {
			t.Fatalf("head %s: %+v", key, info)
		}
		if info.ModTime.Before(start) || info.ModTime.After(time.Now().Add(time.Hour)) {
			t.Fatalf("head %s: implausible mod time %v", key, info.ModTime)
		}
	}
	// replacing write
	mustPut(t, b, "objects/replace", []byte("one"), PutOptions{})
	mustPut(t, b, "objects/replace", []byte("two!"), PutOptions{})
	if got := get(t, b, "objects/replace"); string(got) != "two!" {
		t.Fatalf("replacing put left %q", got)
	}
}

func confSizeExact(t T, b Backend) {
	data := payload(1, 1000)
	if err := b.Put(cbg(), "size/short", bytes.NewReader(data[:999]), 1000, PutOptions{}); err == nil {
		t.Fatal("a reader shorter than size was accepted")
	}
	mustMissing(t, b, "size/short")
	if err := b.Put(cbg(), "size/long", bytes.NewReader(data), 999, PutOptions{}); err == nil {
		t.Fatal("a reader longer than size was accepted")
	}
	mustMissing(t, b, "size/long")
	if err := b.Put(cbg(), "size/neg", bytes.NewReader(nil), -1, PutOptions{}); err == nil {
		t.Fatal("negative size accepted")
	}
	mustMissing(t, b, "size/neg")
	// a failed put must not disturb an existing object either
	mustPut(t, b, "size/keep", []byte("keep"), PutOptions{})
	if err := b.Put(cbg(), "size/keep", bytes.NewReader(data[:10]), 20, PutOptions{}); err == nil {
		t.Fatal("short replacement accepted")
	}
	if got := get(t, b, "size/keep"); string(got) != "keep" {
		t.Fatalf("a failed replacement damaged the object: %q", got)
	}
}

func confCreateOnly(t T, b Backend) {
	mustPut(t, b, "co/a", []byte("first"), PutOptions{IfAbsent: true})
	err := put(t, b, "co/a", []byte("second"), PutOptions{IfAbsent: true})
	if !errors.Is(err, ErrExists) {
		t.Fatalf("second create-only put: want ErrExists, got %v", err)
	}
	if got := get(t, b, "co/a"); string(got) != "first" {
		t.Fatalf("create-only put overwrote the object: %q", got)
	}
	// after a delete the key is free again
	if err := b.Delete(cbg(), "co/a"); err != nil {
		t.Fatal(err)
	}
	mustPut(t, b, "co/a", []byte("third"), PutOptions{IfAbsent: true})
	if got := get(t, b, "co/a"); string(got) != "third" {
		t.Fatalf("got %q", got)
	}
	// an empty object counts as existing
	mustPut(t, b, "co/empty", nil, PutOptions{IfAbsent: true})
	if err := put(t, b, "co/empty", []byte("x"), PutOptions{IfAbsent: true}); !errors.Is(err, ErrExists) {
		t.Fatalf("empty existing object: want ErrExists, got %v", err)
	}
}

func confConcurrentCreateOnly(t T, b Backend) {
	const writers = 16
	for round := 0; round < 3; round++ {
		key := fmt.Sprintf("race/obj-%d", round)
		var wins, exists atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		bodies := make([][]byte, writers)
		errs := make([]error, writers)
		for i := range bodies {
			bodies[i] = payload(int64(100*round+i), 200_000+i*7919) // distinct sizes: a mixed object is detectable
		}
		for i := 0; i < writers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				err := b.Put(cbg(), key, bytes.NewReader(bodies[i]), int64(len(bodies[i])), PutOptions{IfAbsent: true})
				errs[i] = err
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, ErrExists):
					exists.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		if wins.Load() != 1 || exists.Load() != writers-1 {
			t.Fatalf("round %d: %d winners and %d ErrExists among %d writers (want 1 and %d); errors: %v",
				round, wins.Load(), exists.Load(), writers, writers-1, errs)
		}
		got := get(t, b, key)
		matched := -1
		for i := range bodies {
			if bytes.Equal(got, bodies[i]) {
				matched = i
			}
		}
		if matched < 0 {
			t.Fatalf("round %d: the stored object (%d bytes) is not any writer's content (torn or mixed write)", round, len(got))
		}
		if errs[matched] != nil {
			t.Fatalf("round %d: the stored object belongs to writer %d, which was told %v", round, matched, errs[matched])
		}
	}
}

// failingReader delivers n bytes of data and then fails.
type failingReader struct {
	data []byte
	n    int
	err  error
	pos  int
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.pos >= f.n {
		return 0, f.err
	}
	m := copy(p, f.data[f.pos:f.n])
	f.pos += m
	return m, nil
}

func confTornPut(t T, b Backend) {
	boom := errors.New("connection reset")
	data := payload(7, 3<<20)
	for _, ifAbsent := range []bool{true, false} {
		for _, cut := range []int{0, 1, 1 << 20, len(data) - 1} {
			key := fmt.Sprintf("torn/o-%v-%d", ifAbsent, cut)
			err := b.Put(cbg(), key, &failingReader{data: data, n: cut, err: boom}, int64(len(data)), PutOptions{IfAbsent: ifAbsent})
			if err == nil {
				t.Fatalf("put with a reader that failed after %d bytes succeeded", cut)
			}
			mustMissing(t, b, key)
			if keys := listKeys(t, b, "torn/"); len(keys) != 0 {
				t.Fatalf("a torn put left objects behind: %v", keys)
			}
		}
	}
	// the key is usable afterwards
	mustPut(t, b, "torn/ok", []byte("fine"), PutOptions{IfAbsent: true})
}

func confTornReplace(t T, b Backend) {
	old := payload(8, 10_000)
	mustPut(t, b, "tornr/o", old, PutOptions{})
	data := payload(9, 3<<20)
	err := b.Put(cbg(), "tornr/o", &failingReader{data: data, n: 1 << 20, err: errors.New("reset")}, int64(len(data)), PutOptions{})
	if err == nil {
		t.Fatal("torn replacement succeeded")
	}
	if got := get(t, b, "tornr/o"); !bytes.Equal(got, old) {
		t.Fatal("a torn replacement damaged the existing object")
	}
}

func confNotFound(t T, b Backend) {
	for _, key := range []string{"nf/missing", "nf/deep/er/missing", "missing-top"} {
		if _, err := b.Get(cbg(), key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get %q: want ErrNotFound, got %v", key, err)
		}
		if _, err := b.GetRange(cbg(), key, 0, 10); !errors.Is(err, ErrNotFound) {
			t.Fatalf("getrange %q: want ErrNotFound, got %v", key, err)
		}
		mustMissing(t, b, key)
	}
	// a key whose parent "directory" is an object is simply absent
	mustPut(t, b, "nf/obj", []byte("x"), PutOptions{})
	mustMissing(t, b, "nf/obj/child")
	if _, err := b.Get(cbg(), "nf/obj/child"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get below an object: %v", err)
	}
}

func confRange(t T, b Backend) {
	data := payload(11, 100_000)
	mustPut(t, b, "range/o", data, PutOptions{})
	read := func(off, n int64) []byte {
		rc, err := b.GetRange(cbg(), "range/o", off, n)
		if err != nil {
			t.Fatalf("range %d+%d: %v", off, n, err)
		}
		defer rc.Close()
		out, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	size := int64(len(data))
	for _, c := range []struct{ off, n, wantLo, wantHi int64 }{
		{0, 10, 0, 10}, {5, 1, 5, 6}, {99_990, 10, 99_990, 100_000}, {50_000, 20_000, 50_000, 70_000},
		{0, size, 0, size}, {0, -1, 0, size}, {12_345, -1, 12_345, size},
		{size - 1, 1, size - 1, size}, {size - 5, 100, size - 5, size}, // length past the end is cut at the end
		{100, 0, 100, 100},
	} {
		if got := read(c.off, c.n); !bytes.Equal(got, data[c.wantLo:c.wantHi]) {
			t.Fatalf("range off=%d n=%d: got %d bytes, want %d", c.off, c.n, len(got), c.wantHi-c.wantLo)
		}
	}
	// at the end: empty, or an error; never data
	if rc, err := b.GetRange(cbg(), "range/o", size, 10); err == nil {
		out, _ := io.ReadAll(rc)
		rc.Close()
		if len(out) != 0 {
			t.Fatalf("range at the end returned %d bytes", len(out))
		}
	}
	if rc, err := b.GetRange(cbg(), "range/o", size+10, 10); err == nil {
		out, _ := io.ReadAll(rc)
		rc.Close()
		if len(out) != 0 {
			t.Fatalf("range beyond the end returned %d bytes", len(out))
		}
	}
	// the empty object
	mustPut(t, b, "range/empty", nil, PutOptions{})
	if rc, err := b.GetRange(cbg(), "range/empty", 0, -1); err != nil {
		t.Fatalf("range of the empty object: %v", err)
	} else {
		out, _ := io.ReadAll(rc)
		rc.Close()
		if len(out) != 0 {
			t.Fatal("empty object returned data")
		}
	}
}

func confDelete(t T, b Backend) {
	if err := b.Delete(cbg(), "del/never-existed"); err != nil {
		t.Fatalf("deleting a missing key: %v", err)
	}
	mustPut(t, b, "del/a1", []byte("1"), PutOptions{})
	mustPut(t, b, "del/a10", []byte("10"), PutOptions{})
	for i := 0; i < 3; i++ {
		if err := b.Delete(cbg(), "del/a1"); err != nil {
			t.Fatalf("delete #%d: %v", i, err)
		}
	}
	mustMissing(t, b, "del/a1")
	if got := get(t, b, "del/a10"); string(got) != "10" {
		t.Fatal("deleting a1 touched a10")
	}
	if err := b.Delete(cbg(), "del"); err != nil {
		t.Fatalf("deleting a prefix name must be a no-op, got %v", err)
	}
	if got := get(t, b, "del/a10"); string(got) != "10" {
		t.Fatal("deleting a prefix removed objects below it")
	}
}

func confListOrder(t T, b Backend) {
	keys := []string{
		"datastore.json", "chunks/00/00/a", "chunks/00/01/b", "chunks/ab/cd/abcdef", "chunks/ab/cd/abcdff", "chunks/ab/ce/x",
		"groups/g1/s1/index", "groups/g1/s1/manifest", "groups/g1/s2/index", "groups/g2/s1/index",
		"quarantine/q1", "a-b", "a/b", "a.b", "a0", "a b", "Z", "a_b", "a~b", "é", "ü/x",
	}
	for _, k := range keys {
		mustPut(t, b, k, []byte(k), PutOptions{IfAbsent: true})
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	if got := listKeys(t, b, ""); !equalStrings(got, want) {
		t.Fatalf("list all:\n got %q\nwant %q", got, want)
	}
	for _, prefix := range []string{"chunks/", "chunks/ab/", "chunks/ab/cd/abcd", "chunks/a", "groups/g1/", "groups/g1/s1/index", "a", "a/", "a-", "q", "nothing", "chunks/ab/cd/abcdef-no", "é", "datastore.json"} {
		var w []string
		for _, k := range want {
			if strings.HasPrefix(k, prefix) {
				w = append(w, k)
			}
		}
		if got := listKeys(t, b, prefix); !equalStrings(got, w) {
			t.Fatalf("list %q:\n got %q\nwant %q", prefix, got, w)
		}
	}
	// ObjectInfo is filled in
	if err := b.List(cbg(), "groups/g1/s1/", func(o ObjectInfo) error {
		if o.Size != int64(len(o.Key)) || o.ModTime.IsZero() {
			t.Fatalf("list info %+v", o)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func confListStop(t T, b Backend) {
	for i := 0; i < 20; i++ {
		mustPut(t, b, fmt.Sprintf("stop/k%02d", i), []byte("x"), PutOptions{})
	}
	stop := errors.New("enough")
	n := 0
	err := b.List(cbg(), "stop/", func(ObjectInfo) error {
		n++
		if n == 5 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || n != 5 {
		t.Fatalf("list stopped with %v after %d calls, want the callback error after 5", err, n)
	}
}

func confListLarge(t T, b Backend) {
	const n = 2600
	rnd := rand.New(rand.NewSource(42))
	var want []string
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var firstErr atomic.Value
	for i := 0; i < n; i++ {
		var key string
		switch i % 3 {
		case 0:
			key = fmt.Sprintf("big/chunks/%02x/%02x/%08x", rnd.Intn(256), rnd.Intn(256), rnd.Uint32())
		case 1:
			key = fmt.Sprintf("big/groups/g%03d/s%05d/index", i%97, i)
		default:
			key = fmt.Sprintf("big/flat-%05d", i)
		}
		want = append(want, key)
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := b.Put(cbg(), key, strings.NewReader("v"), 1, PutOptions{}); err != nil {
				firstErr.Store(err)
			}
		}()
	}
	wg.Wait()
	if err, _ := firstErr.Load().(error); err != nil {
		t.Fatal(err)
	}
	sort.Strings(want)
	want = dedupSorted(want)
	got := listKeys(t, b, "big/")
	if !equalStrings(got, want) {
		t.Fatalf("listing %d keys returned %d (sorted=%v, duplicates=%v)", len(want), len(got), sort.StringsAreSorted(got), len(dedupSorted(append([]string(nil), got...))) != len(got))
	}
	// a prefix that cuts through the middle of the set
	var half []string
	for _, k := range want {
		if strings.HasPrefix(k, "big/chunks/8") {
			half = append(half, k)
		}
	}
	if got := listKeys(t, b, "big/chunks/8"); !equalStrings(got, half) {
		t.Fatalf("partial prefix: got %d keys, want %d", len(got), len(half))
	}
	// resuming: stop in the middle and list again from the beginning gives the same keys
	cnt := 0
	stop := errors.New("stop")
	_ = b.List(cbg(), "big/", func(ObjectInfo) error {
		cnt++
		if cnt == 1234 {
			return stop
		}
		return nil
	})
	if again := listKeys(t, b, "big/"); !equalStrings(again, want) {
		t.Fatal("listing after an interrupted listing differs")
	}
}

func dedupSorted(s []string) []string {
	out := s[:0]
	for i, k := range s {
		if i == 0 || k != s[i-1] {
			out = append(out, k)
		}
	}
	return out
}

func confAwkwardKeys(t T, b Backend) {
	keys := []string{
		"awk/with space", "awk/plus+sign", "awk/percent%20", "awk/eq=sign", "awk/amp&ersand", "awk/question?mark",
		"awk/hash#tag", "awk/star*", "awk/quote'single", "awk/quote\"double", "awk/colon:name", "awk/at@sign",
		"awk/dollar$", "awk/paren(1)", "awk/brace{x}", "awk/bracket[0]", "awk/semi;colon", "awk/comma,comma", "awk/tilde~",
		"awk/.hidden", "awk/..dots", "awk/dots..", "awk/trailing.", "awk/ünïcödé/日本語/файл", "awk/emoji-\U0001F600",
		"awk/UPPER/Case", "awk/upper/case", "awk/-leading-dash", "awk/w-123", ".dotfile-at-root",
		"awk/" + strings.Repeat("x", 200) + "/" + strings.Repeat("y", 200),
		"awk/" + strings.Repeat("long", 60),
	}
	for i, k := range keys {
		data := []byte(fmt.Sprintf("payload %d for %s", i, k))
		mustPut(t, b, k, data, PutOptions{IfAbsent: true})
	}
	for i, k := range keys {
		want := fmt.Sprintf("payload %d for %s", i, k)
		if got := get(t, b, k); string(got) != want {
			t.Fatalf("key %q: wrong content", k)
		}
		info, err := b.Head(cbg(), k)
		if err != nil || info.Key != k || info.Size != int64(len(want)) {
			t.Fatalf("head %q: %+v %v", k, info, err)
		}
	}
	want := append([]string(nil), keys...)
	sort.Strings(want)
	var withPrefix []string
	for _, k := range want {
		if strings.HasPrefix(k, "awk/") {
			withPrefix = append(withPrefix, k)
		}
	}
	if got := listKeys(t, b, "awk/"); !equalStrings(got, withPrefix) {
		t.Fatalf("listing awkward keys:\n got %q\nwant %q", got, withPrefix)
	}
	for _, k := range keys {
		if err := b.Delete(cbg(), k); err != nil {
			t.Fatal(err)
		}
		mustMissing(t, b, k)
	}
}

func confInvalidKeys(t T, b Backend) {
	bad := []string{"", "/abs", "trail/", "a//b", "a/./b", "a/../b", "../escape", "..", ".", "a/..", "a\\b", "ctl\x01char", "nul\x00byte",
		"tmp/reserved", strings.Repeat(strings.Repeat("k", 200)+"/", 6) + "z", "bad\xffutf8"}
	for _, k := range bad {
		if err := put(t, b, k, []byte("x"), PutOptions{}); err == nil {
			t.Fatalf("put of invalid key %q succeeded", k)
		}
		if _, err := b.Get(cbg(), k); err == nil {
			t.Fatalf("get of invalid key %q succeeded", k)
		}
		if _, err := b.Head(cbg(), k); err == nil {
			t.Fatalf("head of invalid key %q succeeded", k)
		}
		if err := b.Delete(cbg(), k); err == nil {
			t.Fatalf("delete of invalid key %q succeeded", k)
		}
	}
	if keys := listKeys(t, b, ""); len(keys) != 0 {
		t.Fatalf("invalid keys left objects: %q", keys)
	}
	// the longest valid key: components of 200 bytes
	long := strings.Repeat(strings.Repeat("k", 200)+"/", 4) + strings.Repeat("m", MaxKeyLen-804)
	if len(long) != MaxKeyLen {
		t.Fatalf("test bug: %d", len(long))
	}
	mustPut(t, b, long, []byte("max"), PutOptions{})
	if got := get(t, b, long); string(got) != "max" {
		t.Fatal("longest key round trip")
	}
	if err := put(t, b, strings.Repeat("c", MaxComponentLen+1), []byte("x"), PutOptions{}); err == nil {
		t.Fatal("a component longer than MaxComponentLen was accepted")
	}
}

func confReadYourWrites(t T, b Backend) {
	for i := 0; i < 50; i++ {
		key := fmt.Sprintf("ryw/k%03d", i)
		mustPut(t, b, key, []byte("v"), PutOptions{IfAbsent: true})
		if _, err := b.Head(cbg(), key); err != nil {
			t.Fatalf("head right after put: %v", err)
		}
		found := false
		for _, k := range listKeys(t, b, "ryw/k"+fmt.Sprintf("%03d", i)) {
			found = found || k == key
		}
		if !found {
			t.Fatalf("%s is not listed right after its put", key)
		}
		if i%2 == 0 {
			if err := b.Delete(cbg(), key); err != nil {
				t.Fatal(err)
			}
			mustMissing(t, b, key)
			for _, k := range listKeys(t, b, key) {
				if k == key {
					t.Fatalf("%s is still listed after its delete", key)
				}
			}
		}
	}
}

func confContext(t T, b Backend) {
	mustPut(t, b, "ctx/o", []byte("x"), PutOptions{})
	ctx, cancel := context.WithCancel(cbg())
	cancel()
	if err := b.Put(ctx, "ctx/new", strings.NewReader("x"), 1, PutOptions{}); err == nil {
		t.Fatal("put with a cancelled context succeeded")
	}
	mustMissing(t, b, "ctx/new")
	if _, err := b.Get(ctx, "ctx/o"); err == nil {
		t.Fatal("get with a cancelled context succeeded")
	}
	if _, err := b.Head(ctx, "ctx/o"); err == nil {
		t.Fatal("head with a cancelled context succeeded")
	}
	if err := b.List(ctx, "", func(ObjectInfo) error { return nil }); err == nil {
		t.Fatal("list with a cancelled context succeeded")
	}
	// cancelling during a put leaves nothing
	ctx2, cancel2 := context.WithCancel(cbg())
	data := payload(5, 2<<20)
	r := &cancelAfter{data: data, n: 1 << 20, cancel: cancel2}
	if err := b.Put(ctx2, "ctx/mid", r, int64(len(data)), PutOptions{IfAbsent: true}); err == nil {
		t.Fatal("put cancelled in the middle succeeded")
	}
	mustMissing(t, b, "ctx/mid")
}

type cancelAfter struct {
	data   []byte
	n      int
	pos    int
	cancel func()
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	if c.pos >= len(c.data) {
		return 0, io.EOF
	}
	if c.pos >= c.n {
		c.cancel()
		time.Sleep(5 * time.Millisecond)
	}
	m := copy(p, c.data[c.pos:min(len(c.data), c.pos+len(p), c.pos+64<<10)])
	c.pos += m
	return m, nil
}

func confLarge(t T, b Backend) {
	// 24 MiB streamed from a generator: no side holds the whole object in the test
	const size = 24 << 20
	h := sha256.New()
	gen := &genReader{n: size}
	if err := b.Put(cbg(), "large/o", io.TeeReader(gen, h), size, PutOptions{IfAbsent: true}); err != nil {
		t.Fatal(err)
	}
	want := h.Sum(nil)
	rc, err := b.Get(cbg(), "large/o")
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	h2 := sha256.New()
	n, err := io.Copy(h2, rc)
	if err != nil || n != size || !bytes.Equal(h2.Sum(nil), want) {
		t.Fatalf("large object round trip: %d bytes, err %v, digest ok=%v", n, err, bytes.Equal(h2.Sum(nil), want))
	}
	info, err := b.Head(cbg(), "large/o")
	if err != nil || info.Size != size {
		t.Fatalf("%+v %v", info, err)
	}
}

type genReader struct {
	n   int
	pos int
}

func (g *genReader) Read(p []byte) (int, error) {
	if g.pos >= g.n {
		return 0, io.EOF
	}
	m := min(len(p), g.n-g.pos, 100_000)
	for i := 0; i < m; i++ {
		p[i] = byte((g.pos + i) * 31 >> 3)
	}
	g.pos += m
	return m, nil
}
