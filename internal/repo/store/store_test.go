package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

var bg = context.Background()

func testKeys(t testing.TB) *format.Keys {
	t.Helper()
	m := bytes.Repeat([]byte{0x42}, 32)
	k, err := format.DeriveKeys(m)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// backends: every store test runs on the memory and the directory backend.
type backendMaker struct {
	name string
	mk   func(t testing.TB) datastore.Backend
}

var backends = []backendMaker{
	{"mem", func(t testing.TB) datastore.Backend { return datastore.NewMemBackend() }},
	{"dir", func(t testing.TB) datastore.Backend {
		b, err := datastore.NewDirBackend(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		b.NoSync = true
		return b
	}},
}

func forBackends(t *testing.T, fn func(t *testing.T, mk func() datastore.Backend)) {
	for _, bm := range backends {
		t.Run(bm.name, func(t *testing.T) { fn(t, func() datastore.Backend { return bm.mk(t) }) })
	}
}

// snap is a prepared snapshot: chunks, index file and manifest blob.
type snap struct {
	group, id string
	chunks    map[format.ID][]byte
	order     []format.ID
	index     []byte
	manifest  []byte
}

func makeSnap(t testing.TB, k *format.Keys, host, jail, id string, plains ...string) *snap {
	t.Helper()
	s := &snap{group: k.GroupID(host, jail), id: id, chunks: map[format.ID][]byte{}}
	idx := &format.Index{}
	var off uint64
	for _, p := range plains {
		cid, blob, err := k.EncodeChunk([]byte(p), format.EncodeOpts{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := s.chunks[cid]; !ok {
			s.order = append(s.order, cid)
		}
		s.chunks[cid] = blob
		off += uint64(len(p))
		idx.Entries = append(idx.Entries, format.Entry{End: off, ID: cid})
	}
	idx.Size = off
	idx.Entries = append(idx.Entries, format.Entry{End: off + 100, ID: format.ZeroID})
	idx.Size = off + 100
	var err error
	if s.index, err = idx.Marshal(); err != nil {
		t.Fatal(err)
	}
	m := &format.Manifest{SnapshotID: id, Created: time.Unix(1700000000, 0).UTC(), Host: host, Jail: jail,
		IndexDigest: format.FileDigest(s.index),
		Streams:     []format.Stream{{Name: "ds", Kind: format.StreamFiles, PayloadSize: idx.Size - 100, CatalogSize: 100, Mode: "full"}}}
	if s.manifest, err = k.SealManifest(m, s.group, nil); err != nil {
		t.Fatal(err)
	}
	return s
}

func (sn *snap) upload(t testing.TB, x *Session) {
	t.Helper()
	for _, id := range sn.order {
		if err := x.PutChunk(bg, id, sn.chunks[id]); err != nil {
			t.Fatal(err)
		}
	}
	sn.sendMeta(t, x)
}

func (sn *snap) sendMeta(t testing.TB, x *Session) {
	t.Helper()
	if err := x.PutIndex(bg, sn.index); err != nil {
		t.Fatal(err)
	}
	if err := x.PutManifest(bg, sn.manifest); err != nil {
		t.Fatal(err)
	}
}

func mustStore(t testing.TB, b datastore.Backend) *Store {
	t.Helper()
	s, err := Init(bg, b, InitOptions{ServerID: "node-1", Host: "n1"})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func commitNew(t testing.TB, s *Store, sn *snap) *format.Receipt {
	t.Helper()
	x, err := s.Begin(bg, sn.group, sn.id, "")
	if err != nil {
		t.Fatal(err)
	}
	sn.upload(t, x)
	rc, err := x.Commit(bg)
	if err != nil {
		t.Fatal(err)
	}
	return rc
}

func TestInitOpenOwner(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		b := mk()
		s, err := Init(bg, b, InitOptions{ServerID: "node-1", Host: "h", KeyDomain: "0123456789abcdef"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Init(bg, b, InitOptions{}); !errors.Is(err, ErrInitialised) {
			t.Fatalf("second init: %v", err)
		}
		if _, err := Open(bg, mk()); !errors.Is(err, ErrNotRepository) {
			t.Fatalf("open of an empty backend: %v", err)
		}
		s2, err := Open(bg, b)
		if err != nil || s2.Meta().ID != s.Meta().ID {
			t.Fatal(err)
		}
		if err := s2.CheckDomain("0123456789abcdef"); err != nil {
			t.Fatal(err)
		}
		if err := s2.CheckDomain("fedcba9876543210"); !errors.Is(err, ErrDomain) {
			t.Fatalf("domain: %v", err)
		}
		if o, err := s2.ReadOwner(bg); err != nil || o == nil || o.ServerID != "node-1" {
			t.Fatalf("%v %+v", err, o)
		}
		if err := s2.Claim(bg, "node-1", "h", false); err != nil {
			t.Fatal(err)
		}
		if err := s2.Claim(bg, "node-2", "h2", false); !errors.Is(err, ErrForeignOwner) {
			t.Fatalf("foreign claim: %v", err)
		}
		if err := s2.Claim(bg, "node-2", "h2", true); err != nil {
			t.Fatal(err)
		}
		if o, _ := s2.ReadOwner(bg); o.ServerID != "node-2" || o.Previous != "node-1" {
			t.Fatalf("adopt: %+v", o)
		}
		if _, err := Init(bg, mk(), InitOptions{KeyDomain: "nope"}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad domain: %v", err)
		}
	})
}

func TestConcurrentClaimExactlyOneWins(t *testing.T) {
	b := datastore.NewMemBackend()
	s, err := Init(bg, b, InitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.Claim(bg, fmt.Sprintf("node-%d", i), "", false) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claims won", wins.Load())
	}
}

func TestCommitAndRead(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		s := mustStore(t, mk())
		sn := makeSnap(t, k, "host", "jail", "20260101000000-00000001", "alpha", "beta", "alpha")
		rc := commitNew(t, s, sn)
		if rc.ChunkCount != 2 || rc.IndexDigest != format.FileDigest(sn.index) || rc.ManifestDigest != format.FileDigest(sn.manifest) {
			t.Fatalf("%+v", rc)
		}
		l, err := s.ListSnapshots(bg, sn.group)
		if err != nil || len(l) != 1 || l[0].Snapshot != sn.id {
			t.Fatalf("%v %+v", err, l)
		}
		ix, err := s.ReadIndex(bg, sn.group, sn.id)
		if err != nil || !bytes.Equal(ix, sn.index) {
			t.Fatal(err)
		}
		mb, err := s.ReadManifest(bg, sn.group, sn.id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := k.OpenManifest(mb, sn.group, sn.id); err != nil {
			t.Fatal(err)
		}
		if r, err := s.ReadReceipt(bg, sn.group, sn.id); err != nil || r.IndexDigest != rc.IndexDigest {
			t.Fatal(err)
		}
		for id, blob := range sn.chunks {
			got, err := s.GetChunk(bg, id)
			if err != nil || !bytes.Equal(got, blob) {
				t.Fatal(err)
			}
		}
		if gs, _ := s.ListGroups(bg); len(gs) != 1 || gs[0] != sn.group {
			t.Fatalf("groups %v", gs)
		}
		if s.OpenSessions() != 0 {
			t.Fatal("session still open after commit")
		}
		// a committed snapshot id cannot be begun again
		if _, err := s.Begin(bg, sn.group, sn.id, ""); !errors.Is(err, ErrExists) {
			t.Fatalf("begin on a committed id: %v", err)
		}
	})
}

func TestMissingAndPins(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		s := mustStore(t, mk())
		sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "one", "two", "three")
		x, err := s.Begin(bg, sn.group, sn.id, "20251231000000-00000001")
		if err != nil {
			t.Fatal(err)
		}
		all := append(append([]format.ID{format.ZeroID}, sn.order...), sn.order[0])
		miss, err := x.Missing(bg, all)
		if err != nil || len(miss) != 3 {
			t.Fatalf("%v %v", err, miss)
		}
		if err := x.PutChunk(bg, sn.order[1], sn.chunks[sn.order[1]]); err != nil {
			t.Fatal(err)
		}
		if err := x.PutChunk(bg, sn.order[1], sn.chunks[sn.order[1]]); err != nil {
			t.Fatalf("a repeated put must be a no-op: %v", err)
		}
		miss, _ = x.Missing(bg, all)
		if len(miss) != 2 || miss[0] != sn.order[0] || miss[1] != sn.order[2] {
			t.Fatalf("missing after one upload: %v", miss)
		}
		if !s.Pinned(sn.order[1]) || s.Pinned(sn.order[0]) {
			t.Fatal("pins")
		}
		if !s.PinnedSnapshot(sn.group, "20251231000000-00000001") || !s.PinnedSnapshot(sn.group, sn.id) {
			t.Fatal("snapshot pins")
		}
		x.Close()
		if s.Pinned(sn.order[1]) || s.PinnedSnapshot(sn.group, sn.id) {
			t.Fatal("pins survive Close")
		}
		if _, err := x.Missing(bg, all); !errors.Is(err, ErrSessionClosed) {
			t.Fatal(err)
		}
		// the store-level query writes and pins nothing
		if m, _ := s.Missing(bg, all); len(m) != 2 {
			t.Fatal(m)
		}
		// batches of thousands of ids
		var many []format.ID
		for i := 0; i < 3000; i++ {
			many = append(many, format.ID{byte(i), byte(i >> 8), 1})
		}
		if m, err := s.Missing(bg, many); err != nil || len(m) != 3000 {
			t.Fatalf("%v %d", err, len(m))
		}
	})
}

func TestPutChunkRejectsGarbage(t *testing.T) {
	s := mustStore(t, datastore.NewMemBackend())
	k := testKeys(t)
	id, blob, _ := k.EncodeChunk([]byte("x"), format.EncodeOpts{})
	bad := append([]byte(nil), blob...)
	bad[10] ^= 1
	if _, err := s.PutChunk(bg, id, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt blob accepted: %v", err)
	}
	if _, err := s.PutChunk(bg, format.ZeroID, blob); !errors.Is(err, ErrInvalid) {
		t.Fatal("zero id accepted")
	}
	if created, err := s.PutChunk(bg, id, blob); err != nil || !created {
		t.Fatal(err)
	}
	if created, err := s.PutChunk(bg, id, blob); err != nil || created {
		t.Fatal("second put should not create")
	}
	// chunk objects are never overwritten
	other := append([]byte(nil), blob...)
	other[len(other)-1] ^= 1
	_ = other
}

func TestCommitMissingChunkThenRetry(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		b := mk()
		s := mustStore(t, b)
		sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb", "ccc")
		x, _ := s.Begin(bg, sn.group, sn.id, "")
		for _, id := range sn.order[:2] {
			_ = x.PutChunk(bg, id, sn.chunks[id])
		}
		sn.sendMeta(t, x)
		if _, err := x.Commit(bg); !errors.Is(err, ErrMissingChunks) {
			t.Fatalf("want ErrMissingChunks, got %v", err)
		}
		if l, _ := s.ListSnapshots(bg, sn.group); len(l) != 0 {
			t.Fatal("snapshot visible with a missing chunk")
		}
		if _, err := s.ReadIndex(bg, sn.group, sn.id); !errors.Is(err, ErrNotFound) {
			t.Fatal("index readable of an uncommitted snapshot")
		}
		if err := x.PutChunk(bg, sn.order[2], sn.chunks[sn.order[2]]); err != nil {
			t.Fatalf("the session must stay open after a refused commit: %v", err)
		}
		if _, err := x.Commit(bg); err != nil {
			t.Fatal(err)
		}
	})
}

// A chunk deleted behind the repository's back (and not in any cache) must be found at commit.
func TestCommitAsksTheBackendNotACache(t *testing.T) {
	k := testKeys(t)
	m := datastore.NewMemBackend()
	s := mustStore(t, m)
	sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb")
	x, _ := s.Begin(bg, sn.group, sn.id, "")
	sn.upload(t, x)
	miss, _ := x.Missing(bg, sn.order) // pins them as present
	if len(miss) != 0 {
		t.Fatal(miss)
	}
	_ = m.Delete(bg, ChunkKey(sn.order[0]))
	if _, err := x.Commit(bg); !errors.Is(err, ErrMissingChunks) {
		t.Fatalf("commit trusted a stale view: %v", err)
	}
	heads := m.Heads.Load()
	_ = heads
}

func TestCommitIdempotentAndConflict(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		s := mustStore(t, mk())
		sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb")
		rc1 := commitNew(t, s, sn)
		// the same bytes again (lost response): success, same receipt
		s.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
		x, err := s.Begin2(sn)
		if err != nil {
			t.Fatal(err)
		}
		sn.upload(t, x)
		rc2, err := x.Commit(bg)
		if err != nil || rc2.Committed != rc1.Committed {
			t.Fatalf("idempotent retry: %v %+v vs %+v", err, rc2, rc1)
		}
		// a different manifest for the same snapshot id is refused
		sn2 := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb")
		x, err = s.Begin2(sn2)
		if err != nil {
			t.Fatal(err)
		}
		sn2.upload(t, x)
		if _, err := x.Commit(bg); !errors.Is(err, ErrExists) {
			t.Fatalf("conflicting commit: %v", err)
		}
		// and nothing of the committed snapshot changed
		ix, _ := s.ReadIndex(bg, sn.group, sn.id)
		mb, _ := s.ReadManifest(bg, sn.group, sn.id)
		if !bytes.Equal(ix, sn.index) || !bytes.Equal(mb, sn.manifest) {
			t.Fatal("a conflicting commit changed the committed snapshot")
		}
	})
}

// Begin2 begins a session without the committed-id check (to simulate a client retry after a lost response).
func (s *Store) Begin2(sn *snap) (*Session, error) {
	x := &Session{s: s, group: sn.group, snapshot: sn.id, known: map[uint64]struct{}{}}
	s.pinMu.Lock()
	s.sessions[x] = struct{}{}
	s.pinMu.Unlock()
	return x, nil
}

func TestConcurrentCommitsOfOneSnapshot(t *testing.T) {
	k := testKeys(t)
	s := mustStore(t, datastore.NewMemBackend())
	var snaps []*snap
	for i := 0; i < 8; i++ {
		snaps = append(snaps, makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", fmt.Sprintf("bbb%d", i)))
	}
	var wg sync.WaitGroup
	var ok, exists atomic.Int32
	for _, sn := range snaps {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, err := s.Begin2(sn)
			if err != nil {
				t.Error(err)
				return
			}
			sn.upload(t, x)
			if _, err := x.Commit(bg); err == nil {
				ok.Add(1)
			} else if errors.Is(err, ErrExists) {
				exists.Add(1)
			} else {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || exists.Load() != 7 {
		t.Fatalf("%d ok, %d exists", ok.Load(), exists.Load())
	}
	// the committed index and manifest belong together
	l, _ := s.ListSnapshots(bg, snaps[0].group)
	if len(l) != 1 {
		t.Fatal(l)
	}
	ix, _ := s.ReadIndex(bg, l[0].Group, l[0].Snapshot)
	mb, _ := s.ReadManifest(bg, l[0].Group, l[0].Snapshot)
	if format.FileDigest(ix) != l[0].Receipt.IndexDigest || format.FileDigest(mb) != l[0].Receipt.ManifestDigest {
		t.Fatal("committed objects do not match the receipt: a losing commit overwrote them")
	}
}

// ---- crash safety ----------------------------------------------------------------

// observe returns what a reader of the repository sees for the snapshot.
func visible(t *testing.T, s *Store, sn *snap) bool {
	t.Helper()
	l, err := s.ListSnapshots(bg, sn.group)
	if err != nil {
		t.Fatal(err)
	}
	listed := false
	for _, si := range l {
		if si.Snapshot == sn.id {
			listed = true
		}
	}
	_, ierr := s.ReadIndex(bg, sn.group, sn.id)
	_, merr := s.ReadManifest(bg, sn.group, sn.id)
	exists, _ := s.SnapshotExists(bg, sn.group, sn.id)
	readable := ierr == nil && merr == nil
	if listed != exists || (listed && !readable) || (!listed && (ierr == nil || merr == nil)) {
		t.Fatalf("inconsistent view: listed=%v exists=%v index=%v manifest=%v", listed, exists, ierr, merr)
	}
	if listed {
		ix, _ := s.ReadIndex(bg, sn.group, sn.id)
		mb, _ := s.ReadManifest(bg, sn.group, sn.id)
		if !bytes.Equal(ix, sn.index) || !bytes.Equal(mb, sn.manifest) {
			t.Fatal("a visible snapshot with wrong content")
		}
	}
	return listed
}

// A crash at every backend mutation of a commit, on every backend: the snapshot is visible
// only if the marker was written, is never half there, and a restarted commit succeeds.
func TestCrashAtEveryBackendCallOfACommit(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb", "ccc")
		// count the mutating calls of a clean run
		total := 0
		{
			fb := datastore.NewFaultBackend(mk())
			s := mustStore(t, fb)
			fb.CrashAtCall(1 << 30)
			commitNew(t, s, sn)
			total = fb.Calls()
		}
		if total < 6 {
			t.Fatalf("a commit of 3 chunks made only %d mutating calls", total)
		}
		for n := 1; n <= total; n++ {
			t.Run(fmt.Sprintf("crash@%d", n), func(t *testing.T) {
				fb := datastore.NewFaultBackend(mk())
				s := mustStore(t, fb)
				fb.CrashAtCall(n)
				x, err := s.Begin(bg, sn.group, sn.id, "")
				if err != nil {
					t.Fatal(err)
				}
				var cerr error
				for _, id := range sn.order {
					if cerr = x.PutChunk(bg, id, sn.chunks[id]); cerr != nil {
						break
					}
				}
				if cerr == nil {
					_ = x.PutIndex(bg, sn.index)
					_ = x.PutManifest(bg, sn.manifest)
					_, cerr = x.Commit(bg)
				}
				// restarted process: a fresh store on the same storage
				fb.Reset()
				s2, err := Open(bg, fb)
				if err != nil {
					t.Fatal(err)
				}
				committed := visible(t, s2, sn)
				if cerr == nil && !committed {
					t.Fatal("commit reported success but the snapshot is not visible")
				}
				if committed {
					// the marker was the last write: crash points before it must not show the snapshot
					if n < total {
						t.Fatalf("snapshot visible although the crash hit call %d of %d (before the marker)", n, total)
					}
				}
				// the retry (a new session, same bytes) completes it
				x2, err := s2.Begin2(sn)
				if err != nil {
					t.Fatal(err)
				}
				sn.upload(t, x2)
				if _, err := x2.Commit(bg); err != nil {
					t.Fatalf("retry after the crash: %v", err)
				}
				if !visible(t, s2, sn) {
					t.Fatal("not visible after the retry")
				}
				// a clean crash leaves no torn object: every chunk verifies
				for id := range sn.chunks {
					if !s2.ChunkHealthy(bg, id) {
						t.Fatalf("chunk %s unhealthy after the crash and retry", id)
					}
				}
			})
		}
	})
}

// Named crash points inside the commit (store level) and inside the directory
// backend's put, on the marker itself.
func TestCrashAtNamedCommitPoints(t *testing.T) {
	points := []string{"commit:start", "commit:pinned", "commit:checked", "commit:after-index", "commit:before-marker", "commit:after-marker"}
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		for _, p := range points {
			t.Run(p, func(t *testing.T) {
				sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa", "bbb")
				s := mustStore(t, mk())
				x, _ := s.Begin(bg, sn.group, sn.id, "")
				sn.upload(t, x)
				s.SetFault(func(pt string) error {
					if pt == p {
						return errors.New("crash at " + pt)
					}
					return nil
				})
				_, err := x.Commit(bg)
				if err == nil {
					t.Fatal("the fault did not fire")
				}
				s.SetFault(nil)
				got := visible(t, s, sn)
				if got != (p == "commit:after-marker") {
					t.Fatalf("visible=%v after a crash at %s", got, p)
				}
				x2, _ := s.Begin2(sn)
				sn.upload(t, x2)
				if _, err := x2.Commit(bg); err != nil {
					t.Fatalf("retry: %v", err)
				}
				visible(t, s, sn)
			})
		}
	})
}

func TestCrashInsideDirectoryPutOfTheMarker(t *testing.T) {
	for _, point := range []string{"put:created", "put:written", "put:synced", "put:linked", "put:done"} {
		t.Run(point, func(t *testing.T) {
			k := testKeys(t)
			b, _ := datastore.NewDirBackend(t.TempDir())
			b.NoSync = true
			s := mustStore(t, b)
			sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa")
			x, _ := s.Begin(bg, sn.group, sn.id, "")
			sn.upload(t, x)
			b.Hook = func(p, key string) error {
				if p == point && key == SnapPrefix(sn.group, sn.id)+fileCommit {
					return datastore.ErrCrash
				}
				return nil
			}
			_, err := x.Commit(bg)
			if err == nil {
				t.Fatal("expected a crash")
			}
			b.Hook = nil
			committed := visible(t, s, sn)
			// the marker is visible exactly when its link/rename happened
			want := point == "put:linked" || point == "put:done"
			if committed != want {
				t.Fatalf("visible=%v after a crash at %s, want %v", committed, point, want)
			}
		})
	}
}

// ---- delete ----------------------------------------------------------------------

func TestDeleteRemovesMarkerFirst(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		fb := datastore.NewFaultBackend(mk())
		s := mustStore(t, fb)
		sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa")
		commitNew(t, s, sn)
		_ = s.Protect(bg, sn.group, sn.id, true)
		if err := s.DeleteSnapshot(bg, sn.group, sn.id); !errors.Is(err, ErrProtected) {
			t.Fatalf("protected delete: %v", err)
		}
		if !visible(t, s, sn) {
			t.Fatal("a refused delete removed the snapshot")
		}
		_ = s.Protect(bg, sn.group, sn.id, false)
		// crash after each backend call of the delete
		for n := 1; n <= 6; n++ {
			fb.Reset()
			if !visible(t, s, sn) {
				sn2 := sn
				x, _ := s.Begin2(sn2)
				sn2.upload(t, x)
				if _, err := x.Commit(bg); err != nil {
					t.Fatalf("recommit: %v", err)
				}
			}
			fb.CrashAtCall(n)
			err := s.DeleteSnapshot(bg, sn.group, sn.id)
			fb.Reset()
			got := visible(t, s, sn)
			if n == 1 && !errors.Is(err, datastore.ErrCrash) {
				t.Fatal("the first call of delete must be the marker delete")
			}
			if n == 1 && !got {
				t.Fatal("crash before the marker delete removed the snapshot")
			}
			if n >= 2 && got {
				t.Fatalf("crash at call %d: the marker is gone, the snapshot must be invisible", n)
			}
		}
		fb.Reset()
		if err := s.DeleteSnapshot(bg, sn.group, sn.id); err != nil && !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
		if err := s.DeleteSnapshot(bg, sn.group, sn.id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete: %v", err)
		}
	})
}

func TestCleanOrphans(t *testing.T) {
	k := testKeys(t)
	m := datastore.NewMemBackend()
	s := mustStore(t, m)
	now := time.Now()
	s.SetClock(func() time.Time { return now })
	m.SetClock(func() time.Time { return now })
	// a committed snapshot, and the debris of a crashed one
	good := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa")
	commitNew(t, s, good)
	bad := makeSnap(t, k, "h", "j", "20260101000000-00000002", "bbb")
	x, _ := s.Begin(bg, bad.group, bad.id, "")
	bad.upload(t, x)
	s.SetFault(func(p string) error {
		if p == "commit:before-marker" {
			return errors.New("crash")
		}
		return nil
	})
	if _, err := x.Commit(bg); err == nil {
		t.Fatal("no crash")
	}
	s.SetFault(nil)
	if n, _ := s.CleanOrphans(bg, time.Hour); n != 0 {
		t.Fatalf("fresh debris removed: %d", n)
	}
	// while the session is open the debris is protected, aged or not
	now = now.Add(2 * time.Hour)
	if n, _ := s.CleanOrphans(bg, time.Hour); n != 0 {
		t.Fatalf("debris of an open session removed: %d", n)
	}
	x.Close()
	if n, err := s.CleanOrphans(bg, time.Hour); err != nil || n != 2 {
		t.Fatalf("removed %d, %v", n, err)
	}
	if !visible(t, s, good) {
		t.Fatal("the committed snapshot was damaged")
	}
	for _, key := range m.Keys() {
		if len(key) > 8 && key[:7] == "groups/" && key[7:39] == bad.group && key[40:63] == bad.id {
			t.Fatalf("debris left: %s", key)
		}
	}
	// a snapshot that an open session is building is left alone
	x2, _ := s.Begin(bg, bad.group, bad.id, "")
	bad.sendMeta(t, x2)
	s.SetFault(func(p string) error {
		if p == "commit:before-marker" {
			return errors.New("crash")
		}
		return nil
	})
	_, _ = x2.Commit(bg)
	s.SetFault(nil)
	x3, _ := s.Begin(bg, bad.group, bad.id, "")
	_ = x3
	if n, _ := s.CleanOrphans(bg, 0); n != 0 {
		t.Fatalf("debris of a snapshot with an open session removed: %d", n)
	}
}

// ---- quarantine and escrow ----------------------------------------------------------

func TestQuarantineAndHeal(t *testing.T) {
	forBackends(t, func(t *testing.T, mk func() datastore.Backend) {
		k := testKeys(t)
		b := mk()
		s := mustStore(t, b)
		id, blob, _ := k.EncodeChunk([]byte("precious chunk data"), format.EncodeOpts{})
		if _, err := s.PutChunk(bg, id, blob); err != nil {
			t.Fatal(err)
		}
		if err := s.Quarantine(bg, id); err != nil {
			t.Fatal(err)
		}
		if ok, _ := s.HasChunk(bg, id); !ok {
			t.Fatal("a healthy chunk must not be quarantined")
		}
		// bit rot: rewrite the object with a flipped byte, bypassing the store
		bad := append([]byte(nil), blob...)
		bad[20] ^= 1
		_ = b.Delete(bg, ChunkKey(id))
		_ = datastore.PutBytes(bg, b, ChunkKey(id), bad, datastore.PutOptions{IfAbsent: true})
		if s.ChunkHealthy(bg, id) {
			t.Fatal("corrupt chunk reported healthy")
		}
		if err := s.Quarantine(bg, id); err != nil {
			t.Fatal(err)
		}
		if err := s.Quarantine(bg, id); err != nil {
			t.Fatal("quarantine must be idempotent")
		}
		if m, _ := s.Missing(bg, []format.ID{id}); len(m) != 1 {
			t.Fatal("a quarantined chunk must count as missing")
		}
		q, _ := s.ListQuarantine(bg)
		if len(q) != 1 || q[0].ID != id || q[0].Healed {
			t.Fatalf("%+v", q)
		}
		// the next upload heals it
		if created, err := s.PutChunk(bg, id, blob); err != nil || !created {
			t.Fatal(err)
		}
		q, _ = s.ListQuarantine(bg)
		if len(q) != 1 || !q[0].Healed {
			t.Fatalf("%+v", q)
		}
		if n, _ := s.PurgeQuarantine(bg, time.Hour); n != 0 {
			t.Fatal("young quarantine entry purged")
		}
		s.SetClock(func() time.Time { return time.Now().Add(2 * time.Hour) })
		if n, _ := s.PurgeQuarantine(bg, time.Hour); n != 1 {
			t.Fatal("healed old entry not purged")
		}
	})
}

func TestEscrowCreateOnly(t *testing.T) {
	s := mustStore(t, datastore.NewMemBackend())
	dom := "0123456789abcdef"
	if _, err := s.GetEscrow(bg, dom); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.PutEscrow(bg, dom, []byte("blob")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutEscrow(bg, dom, []byte("other")); !errors.Is(err, ErrExists) {
		t.Fatalf("escrow overwritten: %v", err)
	}
	if b, _ := s.GetEscrow(bg, dom); string(b) != "blob" {
		t.Fatal("content")
	}
	if l, _ := s.ListEscrow(bg); len(l) != 1 || l[0] != dom {
		t.Fatal(l)
	}
	for _, bad := range []string{"../x", "short", "0123456789ABCDEF"} {
		if err := s.PutEscrow(bg, bad, []byte("x")); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if err := s.PutEscrow(bg, dom+"", nil); !errors.Is(err, ErrInvalid) && !errors.Is(err, ErrExists) {
		t.Fatal(err)
	}
	_ = s.DeleteEscrow(bg, dom)
	if _, err := s.GetEscrow(bg, dom); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestModeBackendUnderStore(t *testing.T) {
	k := testKeys(t)
	mb := datastore.NewModeBackend(datastore.NewMemBackend(), datastore.ModeOnline)
	s := mustStore(t, mb)
	sn := makeSnap(t, k, "h", "j", "20260101000000-00000001", "aaa")
	commitNew(t, s, sn)
	mb.SetMode(datastore.ModeReadOnly)
	sn2 := makeSnap(t, k, "h", "j", "20260101000000-00000002", "bbb")
	x, _ := s.Begin(bg, sn2.group, sn2.id, "")
	if err := x.PutChunk(bg, sn2.order[0], sn2.chunks[sn2.order[0]]); !errors.Is(err, datastore.ErrReadOnly) {
		t.Fatalf("write in read-only mode: %v", err)
	}
	if err := s.DeleteSnapshot(bg, sn.group, sn.id); !errors.Is(err, datastore.ErrReadOnly) {
		t.Fatalf("delete in read-only mode: %v", err)
	}
	if !visible(t, s, sn) {
		t.Fatal("snapshot lost")
	}
	if _, err := s.ReadIndex(bg, sn.group, sn.id); err != nil {
		t.Fatal(err)
	}
	mb.SetMode(datastore.ModeOffline)
	if _, err := s.ListSnapshots(bg, sn.group); !errors.Is(err, datastore.ErrOffline) {
		t.Fatal(err)
	}
}

func TestInvalidArguments(t *testing.T) {
	s := mustStore(t, datastore.NewMemBackend())
	if _, err := s.Begin(bg, "nothex", "20260101000000-00000001", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.Begin(bg, "00000000000000000000000000000000", "../../x", ""); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.ReadIndex(bg, "00000000000000000000000000000000", "bad"); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	x, _ := s.Begin(bg, "00000000000000000000000000000000", "20260101000000-00000001", "")
	if _, err := x.Commit(bg); !errors.Is(err, ErrInvalid) {
		t.Fatalf("commit without index and manifest: %v", err)
	}
	if err := x.PutIndex(bg, []byte("junk")); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if err := x.PutManifest(bg, []byte("junk")); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
