// Package nodetest builds backup nodes and backup content for tests: a node on a
// fresh PostgreSQL database and an in-memory (or any) repository, a controllable
// clock, enrolled hosts, and synthetic encrypted backups made with the real
// format package, as a host would make them.
//
// Test binaries using it must call testdb.Main from TestMain.
package nodetest

import (
	"bytes"
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"jailops.net/jailops/internal/node"
	"jailops.net/jailops/internal/node/ctl"
	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
	"jailops.net/jailops/internal/testdb"
)

// Clock is a clock tests move by hand.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

// NewClock starts at the real time, so that backends that stamp objects with the wall clock (a directory, S3) agree with it.
func NewClock() *Clock { return &Clock{t: time.Now().UTC().Truncate(time.Second)} }

// Now implements the clock function of the node and of MemBackend.
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

// Advance moves the clock.
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// Env is a node under test.
type Env struct {
	Node  *node.Node
	DB    *ctl.DB
	Raw   datastore.Backend
	Mem   *datastore.MemBackend // nil when a backend was supplied
	Clock *Clock
	Cfg   node.Config
	hosts int
}

// Options for NewEnv.
type Options struct {
	// Backend is the repository storage; nil means a MemBackend on the test clock.
	Backend datastore.Backend
	// Configure adjusts the node configuration.
	Configure func(*node.Config)
	// NoInit skips creating the repository (the backend already holds one).
	NoInit bool
	// DB reuses a control plane (a second node on the same database).
	DB *ctl.DB
}

// NewEnv creates the database, the repository and the node.
func NewEnv(t testing.TB, o Options) *Env {
	t.Helper()
	ctx := context.Background()
	e := &Env{Clock: NewClock()}
	e.DB = o.DB
	if e.DB == nil {
		db, err := ctl.Open(ctx, testdb.New(t))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		if err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		e.DB = db
	}
	e.Raw = o.Backend
	if e.Raw == nil {
		e.Mem = datastore.NewMemBackend()
		e.Mem.SetClock(e.Clock.Now)
		e.Raw = e.Mem
	}
	if !o.NoInit {
		if _, err := node.Init(ctx, e.Raw, node.InitOptions{ServerID: "node-test", Host: "testhost"}); err != nil {
			t.Fatal(err)
		}
	}
	e.Cfg = node.Config{Backend: e.Raw, DB: e.DB, ServerID: "node-test", Hostname: "testhost", Now: e.Clock.Now,
		MinGrace: time.Minute, Grace: time.Hour, Parallelism: 8}
	if o.Configure != nil {
		o.Configure(&e.Cfg)
	}
	n, err := node.Open(ctx, e.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	e.Node = n
	return e
}

// Reopen closes the node and opens a new one on the same repository and database (a restart).
func (e *Env) Reopen(t testing.TB) {
	t.Helper()
	e.Node.Close()
	n, err := node.Open(context.Background(), e.Cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	e.Node = n
}

// Host enrols a host with push and pull scopes and returns its identity.
func (e *Env) Host(t testing.TB, name string, scopes ...string) node.Host {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{"backup-push", "backup-pull"}
	}
	e.hosts++
	fp := fmt.Sprintf("%064x", e.hosts)
	h, err := e.DB.EnrollHost(context.Background(), name, fp, scopes, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	return node.Host{ID: h.ID, Name: h.Name, Scopes: h.Scopes}
}

// Fixture makes encrypted backup content.
type Fixture struct {
	Keys *format.Keys
	rnd  *rand.Rand
}

// NewFixture derives test keys from a seed.
func NewFixture(t testing.TB, seed int64) *Fixture {
	t.Helper()
	master := bytes.Repeat([]byte{byte(seed), 7}, 16)
	k, err := format.DeriveKeys(master)
	if err != nil {
		t.Fatal(err)
	}
	return &Fixture{Keys: k, rnd: rand.New(rand.NewSource(seed))}
}

// Group returns a group id for a (host, jail) pair.
func (f *Fixture) Group(host, jail string) string { return f.Keys.GroupID(host, jail) }

// Chunk is one encrypted chunk.
type Chunk struct {
	ID   format.ID
	Blob []byte
	Size int // plaintext size
}

// NewChunk makes a chunk of random plaintext.
func (f *Fixture) NewChunk(t testing.TB) Chunk {
	t.Helper()
	pt := make([]byte, 2000+f.rnd.Intn(4000))
	f.rnd.Read(pt)
	id, blob, err := f.Keys.EncodeChunk(pt, format.EncodeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	return Chunk{ID: id, Blob: blob, Size: len(pt)}
}

// Backup is everything a host uploads for one snapshot.
type Backup struct {
	Group, Snapshot string
	Chunks          []Chunk
	Index, Manifest []byte
	IndexDigest     string
	ManifestDigest  string
}

// NewBackup assembles a backup of the given chunks.
func (f *Fixture) NewBackup(t testing.TB, group, snapshot string, chunks []Chunk) *Backup {
	t.Helper()
	idx := &format.Index{}
	var pos uint64
	for _, c := range chunks {
		pos += uint64(c.Size)
		idx.Entries = append(idx.Entries, format.Entry{End: pos, ID: c.ID})
	}
	idx.Size = pos
	ib, err := idx.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	m := &format.Manifest{SnapshotID: snapshot, Created: time.Now().UTC(), Host: "host", Jail: "jail", IndexDigest: format.FileDigest(ib),
		Streams: []format.Stream{{Name: "tank/jail", Kind: format.StreamFiles, PayloadSize: pos, Mode: "full"}}}
	mb, err := f.Keys.SealManifest(m, group, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Backup{Group: group, Snapshot: snapshot, Chunks: chunks, Index: ib, Manifest: mb, IndexDigest: format.FileDigest(ib), ManifestDigest: format.FileDigest(mb)}
}

// SnapshotID returns a snapshot id for a time, unique by n.
func SnapshotID(t time.Time, n int) string {
	return fmt.Sprintf("%s-%08x", t.UTC().Format("20060102150405"), n)
}

// IDs returns the chunk ids of the backup.
func (b *Backup) IDs() []format.ID {
	out := make([]format.ID, len(b.Chunks))
	for i, c := range b.Chunks {
		out[i] = c.ID
	}
	return out
}

// Upload runs the whole host protocol against the node library: begin, missing,
// chunks, index, manifest, commit. It returns the receipt.
func (b *Backup) Upload(t testing.TB, n *node.Node, h node.Host, previous string) *format.Receipt {
	t.Helper()
	ctx := context.Background()
	si, err := n.Begin(ctx, h, b.Group, b.Snapshot, previous)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	missing, err := n.Missing(ctx, h, si.ID, b.IDs())
	if err != nil {
		t.Fatalf("missing: %v", err)
	}
	want := map[format.ID]bool{}
	for _, m := range missing {
		want[m] = true
	}
	for _, c := range b.Chunks {
		if !want[c.ID] {
			continue
		}
		if _, err := n.PutChunk(ctx, h, si.ID, c.ID, bytes.NewReader(c.Blob), int64(len(c.Blob))); err != nil {
			t.Fatalf("put chunk: %v", err)
		}
	}
	if _, _, err := n.PutIndex(ctx, h, si.ID, bytes.NewReader(b.Index), int64(len(b.Index))); err != nil {
		t.Fatalf("put index: %v", err)
	}
	if _, err := n.PutManifest(ctx, h, si.ID, bytes.NewReader(b.Manifest), int64(len(b.Manifest))); err != nil {
		t.Fatalf("put manifest: %v", err)
	}
	rc, err := n.Commit(ctx, h, si.ID, node.CommitRequest{IndexDigest: b.IndexDigest, ManifestDigest: b.ManifestDigest})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return rc
}
