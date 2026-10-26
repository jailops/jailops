package s3fake

import (
	"crypto/md5"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	bucketNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	ipRE         = regexp.MustCompile(`^(\d+\.){3}\d+$`)
)

func validBucketName(n string) bool {
	return bucketNameRE.MatchString(n) && !ipRE.MatchString(n) && !strings.Contains(n, "..")
}

type object struct {
	data []byte
	mod  time.Time
	etag string
}

type bucket struct {
	name    string
	created time.Time
	mu      sync.RWMutex
	objs    map[string]*object
	keys    []string // sorted keys of objs
}

type store struct {
	mu      sync.RWMutex
	buckets map[string]*bucket
}

func newStore() *store { return &store{buckets: map[string]*bucket{}} }

func (st *store) bucket(name string) *bucket {
	st.mu.RLock()
	defer st.mu.RUnlock()
	return st.buckets[name]
}

func (st *store) names() []string {
	st.mu.RLock()
	defer st.mu.RUnlock()
	out := make([]string, 0, len(st.buckets))
	for n := range st.buckets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// create makes a bucket; false if it exists.
func (st *store) create(name string) (*bucket, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if b, ok := st.buckets[name]; ok {
		return b, false
	}
	b := &bucket{name: name, objs: map[string]*object{}, created: time.Now().UTC().Truncate(time.Millisecond)}
	st.buckets[name] = b
	return b, true
}

func (st *store) remove(name string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	delete(st.buckets, name)
}

// put stores an object; the caller holds b.mu for writing. Only a complete
// body ever reaches here: an upload is collected first and published in one step.
func (b *bucket) put(key string, data []byte, mod time.Time) {
	sum := md5.Sum(data)
	if _, ok := b.objs[key]; !ok {
		i := sort.SearchStrings(b.keys, key)
		b.keys = append(b.keys, "")
		copy(b.keys[i+1:], b.keys[i:])
		b.keys[i] = key
	}
	b.objs[key] = &object{data: data, mod: mod.UTC().Truncate(time.Millisecond), etag: hex.EncodeToString(sum[:])}
}

// remove deletes an object; the caller holds b.mu for writing.
func (b *bucket) remove(key string) {
	if _, ok := b.objs[key]; !ok {
		return
	}
	delete(b.objs, key)
	i := sort.SearchStrings(b.keys, key)
	if i < len(b.keys) && b.keys[i] == key {
		b.keys = append(b.keys[:i], b.keys[i+1:]...)
	}
}
