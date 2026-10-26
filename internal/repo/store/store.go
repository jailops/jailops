// Package store is the backup repository object model over a datastore.Backend.
//
// The repository makes no POSIX assumptions: keys are flat strings, objects
// are immutable once written (writes are create-only, except the documented
// replaceable ones), listing is by prefix. It never sees a key: integrity is
// checked with the SHA-256 trailers of the blobs, references are understood
// through the plaintext indexes.
//
// Key layout (the same on a directory and on S3):
//
//	datastore.json                          format version, id, created
//	owner.json                              the backup node that owns the repository
//	chunks/aa/bb/<64-hex id>                one encrypted chunk blob, immutable
//	quarantine/<64-hex id>                  a corrupt blob moved out of chunks/
//	escrow/<16-hex domain>.age              the master key wrapped to offline recipients
//	groups/<group>/<snap>/index             plaintext index
//	groups/<group>/<snap>/manifest          encrypted manifest
//	groups/<group>/<snap>/commit.json       receipt, written last
//	groups/<group>/<snap>/protected         marker: retention and delete refuse
//	groups/<group>/<snap>/notes             free text
//
// Atomic visibility: a snapshot exists if and only if its commit.json exists.
// It is written last, create-only, after the chunks, the index and the manifest
// are durable. A crash at any earlier point leaves invisible debris.
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

// Errors.
var (
	// ErrExists: the snapshot is already committed (with different content).
	ErrExists = errors.New("store: snapshot already exists")
	// ErrNotFound: unknown chunk, snapshot or object.
	ErrNotFound = errors.New("store: not found")
	// ErrInvalid marks a malformed argument (a caller error, not a storage fault).
	ErrInvalid = errors.New("store: invalid argument")
	// ErrProtected: removing a protected snapshot.
	ErrProtected = errors.New("store: snapshot is protected")
	// ErrMissingChunks: Commit found chunks the repository lacks. The session
	// stays open so they can be uploaded and the commit retried.
	ErrMissingChunks = errors.New("store: snapshot references missing chunks")
	// ErrInitialised: Init on a backend that already holds a repository.
	ErrInitialised = errors.New("store: the backend already holds a repository")
	// ErrNotRepository: Open on a backend without datastore.json.
	ErrNotRepository = errors.New("store: not a repository")
	// ErrForeignOwner: the repository belongs to another node.
	ErrForeignOwner = errors.New("store: repository belongs to another node")
	// ErrSessionClosed: a session used after Commit, Abort or Close.
	ErrSessionClosed = errors.New("store: session is closed")
	// ErrDomain: the key domain of the caller is not the repository's.
	ErrDomain = errors.New("store: the key domain does not match the repository")
)

// Limits.
const (
	maxMetaBytes     = 1 << 20
	maxManifestBytes = format.MaxManifestSize
	maxReceiptBytes  = 64 << 10
	// CheckParallelism is how many HEAD requests run at once.
	CheckParallelism = 64
)

// Meta is datastore.json.
type Meta struct {
	Format  int       `json:"format"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	// KeyDomain is the non-secret domain id of the master key, when the
	// initialiser knew it ("" otherwise). Hosts holding another key are refused.
	KeyDomain string `json:"key_domain,omitempty"`
}

// Owner is owner.json.
type Owner struct {
	ServerID string    `json:"server_id"`
	Host     string    `json:"host,omitempty"`
	Since    time.Time `json:"since"`
	// Previous is the owner this one replaced when it was explicitly adopted.
	Previous string `json:"previous,omitempty"`
}

const (
	metaKey  = "datastore.json"
	ownerKey = "owner.json"
)

// Store is an open repository.
type Store struct {
	b    datastore.Backend
	meta Meta
	now  func() time.Time

	// fault, when set, is consulted at named crash points (tests only).
	fault func(point string) error

	// pins: chunks that open sessions rely on, and the snapshots they are based on.
	pinMu    sync.Mutex
	sessions map[*Session]struct{}

	// snapLocks serialise, per snapshot directory, commit and delete against
	// the orphan cleaner, which decide from an earlier look at the same keys.
	snapLocks [64]sync.Mutex
}

// InitOptions describe a new repository.
type InitOptions struct {
	// ServerID, if set, claims owner.json for this node.
	ServerID string
	Host     string
	// KeyDomain, if set, is recorded in datastore.json.
	KeyDomain string
}

// Init creates a repository on b, which must not hold one yet.
func Init(ctx context.Context, b datastore.Backend, o InitOptions) (*Store, error) {
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	m := Meta{Format: 1, ID: id, Created: time.Now().UTC(), KeyDomain: o.KeyDomain}
	if o.KeyDomain != "" && !format.ValidDomainID(o.KeyDomain) {
		return nil, fmt.Errorf("%w: key domain", ErrInvalid)
	}
	data, _ := json.MarshalIndent(m, "", "  ")
	data = append(data, '\n')
	if err := datastore.PutBytes(ctx, b, metaKey, data, datastore.PutOptions{IfAbsent: true}); err != nil {
		if errors.Is(err, datastore.ErrExists) {
			return nil, ErrInitialised
		}
		return nil, err
	}
	s := newStore(b, m)
	if o.ServerID != "" {
		if err := s.Claim(ctx, o.ServerID, o.Host, false); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// Open opens an existing repository. It does not check ownership.
func Open(ctx context.Context, b datastore.Backend) (*Store, error) {
	data, err := datastore.ReadAll(ctx, b, metaKey, maxMetaBytes)
	if err != nil {
		if errors.Is(err, datastore.ErrNotFound) {
			return nil, ErrNotRepository
		}
		return nil, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil || m.Format != 1 {
		return nil, fmt.Errorf("%w: unsupported or damaged %s", ErrNotRepository, metaKey)
	}
	return newStore(b, m), nil
}

func newStore(b datastore.Backend, m Meta) *Store {
	return &Store{b: b, meta: m, now: time.Now, sessions: map[*Session]struct{}{}}
}

// Backend returns the storage the repository lives on.
func (s *Store) Backend() datastore.Backend { return s.b }

// Meta returns datastore.json as read at Open or Init.
func (s *Store) Meta() Meta { return s.meta }

// SetClock replaces the clock used for receipts (tests).
func (s *Store) SetClock(f func() time.Time) { s.now = f }

// SetFault installs a crash-point hook (tests only).
func (s *Store) SetFault(f func(point string) error) { s.fault = f }

func (s *Store) at(point string) error {
	if s.fault == nil {
		return nil
	}
	return s.fault(point)
}

// CheckDomain refuses a key domain different from the one the repository was
// initialised with. A repository that recorded none accepts any.
func (s *Store) CheckDomain(domain string) error {
	if s.meta.KeyDomain != "" && s.meta.KeyDomain != domain {
		return fmt.Errorf("%w (repository %s, key %s)", ErrDomain, s.meta.KeyDomain, domain)
	}
	return nil
}

// ---- ownership ----------------------------------------------------------------

// ReadOwner returns owner.json, or nil when the repository has no owner yet.
func (s *Store) ReadOwner(ctx context.Context) (*Owner, error) {
	data, err := datastore.ReadAll(ctx, s.b, ownerKey, maxMetaBytes)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var o Owner
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", ownerKey, err)
	}
	return &o, nil
}

// Claim makes serverID the owner. A repository with no owner is claimed with a
// create-only write (two nodes racing: exactly one wins). One owned by another
// node is refused unless adopt is set, which replaces the owner and records who
// it was. owner.json is the one object that is replaceable.
func (s *Store) Claim(ctx context.Context, serverID, host string, adopt bool) error {
	for attempt := 0; attempt < 3; attempt++ {
		cur, err := s.ReadOwner(ctx)
		if err != nil {
			return err
		}
		mine := Owner{ServerID: serverID, Host: host, Since: s.now().UTC()}
		write := func(ifAbsent bool) error {
			data, _ := json.Marshal(mine)
			return datastore.PutBytes(ctx, s.b, ownerKey, data, datastore.PutOptions{IfAbsent: ifAbsent})
		}
		switch {
		case cur == nil:
			err := write(true)
			if errors.Is(err, datastore.ErrExists) {
				continue // lost the race: look at who won
			}
			return err
		case cur.ServerID == serverID:
			return nil
		case adopt:
			mine.Previous = cur.ServerID
			return write(false)
		default:
			return fmt.Errorf("%w (owner %s since %s, host %q); adopt it only if that node is gone for good",
				ErrForeignOwner, cur.ServerID, cur.Since.Format(time.RFC3339), cur.Host)
		}
	}
	return errors.New("store: could not settle ownership")
}

// ---- keys ----------------------------------------------------------------------

// ValidGroup reports whether g is a well-formed group id.
func ValidGroup(g string) bool { return format.ValidGroupID(g) }

// ChunkKey is the object key of a chunk.
func ChunkKey(id format.ID) string {
	h := id.String()
	return "chunks/" + h[:2] + "/" + h[2:4] + "/" + h
}

func quarantineKey(id format.ID) string { return "quarantine/" + id.String() }

// SnapPrefix is the key prefix of one snapshot, with the trailing slash.
func SnapPrefix(group, snap string) string { return "groups/" + group + "/" + snap + "/" }

// Object names inside a snapshot.
const (
	fileIndex   = "index"
	fileManif   = "manifest"
	fileCommit  = "commit.json"
	fileProt    = "protected"
	fileNotes   = "notes"
	fileVerify  = "verify.json"
	escrowExt   = ".age"
	escrowDir   = "escrow/"
	groupPrefix = "groups/"
)

func checkIDs(group, snap string) error {
	if !ValidGroup(group) || !format.ValidSnapshotID(snap) {
		return fmt.Errorf("%w: invalid group or snapshot id", ErrInvalid)
	}
	return nil
}

func (s *Store) snapLock(group, snap string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(group + "/" + snap))
	return &s.snapLocks[h.Sum32()%uint32(len(s.snapLocks))]
}

func randomID() (string, error) {
	g, err := format.GenerateMaster()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", g[:8]), nil
}

func equalBytes(a, b []byte) bool { return bytes.Equal(a, b) }
