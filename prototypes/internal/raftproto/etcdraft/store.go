package etcdraft

import (
	"encoding/binary"

	bolt "go.etcd.io/bbolt"
	raft "go.etcd.io/raft/v3"
	pb "go.etcd.io/raft/v3/raftpb"
	"google.golang.org/protobuf/proto"
)

// The etcd raft library persists nothing. This file is the adapter's durable log: a bbolt file
// with one bucket of entries keyed by big-endian index, plus HardState, the latest snapshot and
// the peer address book in a meta bucket. One bolt transaction (one fsync) per Ready.
var (
	bEnt  = []byte("ent")
	bMeta = []byte("meta")
	kHS   = []byte("hardstate")
	kSnap = []byte("snapshot")
	kPeer = []byte("peers")
)

func idx(i uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, i)
	return b
}

type store struct {
	db *bolt.DB
}

func openStore(path string, noSync bool) (*store, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{NoSync: noSync, NoFreelistSync: false})
	if err != nil {
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if _, err := tx.CreateBucketIfNotExists(bEnt); err != nil {
			return err
		}
		_, err := tx.CreateBucketIfNotExists(bMeta)
		return err
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

// loaded is what is on disk.
type loaded struct {
	hs    *pb.HardState
	snap  *pb.Snapshot
	ents  []*pb.Entry
	peers []byte
}

func (s *store) load() (loaded, error) {
	var l loaded
	err := s.db.View(func(tx *bolt.Tx) error {
		m := tx.Bucket(bMeta)
		if v := m.Get(kHS); v != nil {
			l.hs = &pb.HardState{}
			if err := proto.Unmarshal(v, l.hs); err != nil {
				return err
			}
		}
		if v := m.Get(kSnap); v != nil {
			l.snap = &pb.Snapshot{}
			if err := proto.Unmarshal(v, l.snap); err != nil {
				return err
			}
		}
		if v := m.Get(kPeer); v != nil {
			l.peers = append([]byte(nil), v...)
		}
		return tx.Bucket(bEnt).ForEach(func(k, v []byte) error {
			e := &pb.Entry{}
			if err := proto.Unmarshal(v, e); err != nil {
				return err
			}
			l.ents = append(l.ents, e)
			return nil
		})
	})
	return l, err
}

// save persists one Ready. When snap is non-empty (installed from the leader) every older
// entry is dropped first, because raft discards the whole log below a received snapshot.
func (s *store) save(hs *pb.HardState, ents []*pb.Entry, snap *pb.Snapshot) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		eb, mb := tx.Bucket(bEnt), tx.Bucket(bMeta)
		if snap != nil && !raft.IsEmptySnap(snap) {
			v, err := proto.Marshal(snap)
			if err != nil {
				return err
			}
			if err := mb.Put(kSnap, v); err != nil {
				return err
			}
			c := eb.Cursor()
			for k, _ := c.First(); k != nil; k, _ = c.First() {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		if len(ents) > 0 {
			first := ents[0].GetIndex()
			c := eb.Cursor()
			for k, _ := c.Seek(idx(first)); k != nil; k, _ = c.Seek(idx(first)) {
				if err := c.Delete(); err != nil {
					return err
				}
			}
			for _, e := range ents {
				v, err := proto.Marshal(e)
				if err != nil {
					return err
				}
				if err := eb.Put(idx(e.GetIndex()), v); err != nil {
					return err
				}
			}
		}
		if hs != nil && !raft.IsEmptyHardState(hs) {
			v, err := proto.Marshal(hs)
			if err != nil {
				return err
			}
			return mb.Put(kHS, v)
		}
		return nil
	})
}

// saveSnapshot stores a locally created snapshot and deletes entries below compactIdx.
func (s *store) saveSnapshot(snap *pb.Snapshot, compactIdx uint64) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		v, err := proto.Marshal(snap)
		if err != nil {
			return err
		}
		if err := tx.Bucket(bMeta).Put(kSnap, v); err != nil {
			return err
		}
		c := tx.Bucket(bEnt).Cursor()
		for k, _ := c.First(); k != nil && binary.BigEndian.Uint64(k) < compactIdx; k, _ = c.First() {
			if err := c.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *store) savePeers(b []byte) error {
	return s.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bMeta).Put(kPeer, b) })
}
