package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"jailops.net/jailops/internal/repo/datastore"
	"jailops.net/jailops/internal/repo/format"
)

// Object names of the repository.
const (
	metaKey    = "datastore.json"
	ownerKey   = "owner.json"
	chunkDir   = "chunks/"
	quarDir    = "quarantine/"
	groupsDir  = "groups/"
	fileIndex  = "index"
	fileManif  = "manifest"
	fileCommit = "commit.json"
	fileProt   = "protected"
	fileDelete = "deletion.json"
	fileVerify = "verify.json"
	fileNotes  = "notes"

	maxMeta = 1 << 20
)

func chunkKey(id format.ID) string {
	h := id.String()
	return chunkDir + h[:2] + "/" + h[2:4] + "/" + h
}

func quarantineKey(id format.ID) string { return quarDir + id.String() }

func snapPrefix(group, snap string) string { return groupsDir + group + "/" + snap + "/" }

// chunkIDFromKey parses chunks/aa/bb/<id> and checks that the fan-out matches the id.
func chunkIDFromKey(key string) (format.ID, bool) {
	rest, ok := strings.CutPrefix(key, chunkDir)
	if !ok {
		return format.ID{}, false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || len(parts[2]) != 64 || parts[2][:2] != parts[0] || parts[2][2:4] != parts[1] {
		return format.ID{}, false
	}
	id, err := format.ParseID(parts[2])
	return id, err == nil
}

// Meta is datastore.json.
type Meta struct {
	Format  int       `json:"format"`
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	// KeyDomain is the non-secret domain id of the master key, when the initialiser knew it.
	KeyDomain string `json:"key_domain,omitempty"`
}

// Owner is owner.json: the node that owns the repository. Two nodes pointed at
// one repository must not both run garbage collection and retention.
type Owner struct {
	ServerID string    `json:"server_id"`
	Host     string    `json:"host,omitempty"`
	Since    time.Time `json:"since"`
	// Previous is the owner this node replaced when it was explicitly adopted.
	Previous string `json:"previous,omitempty"`
}

// ErrForeignOwner is returned when another node owns the repository.
var ErrForeignOwner = errors.New("node: the repository belongs to another backup node")

// ErrNotRepository is returned when the datastore holds no repository.
var ErrNotRepository = errors.New("node: the datastore holds no repository (run init)")

func readJSON(ctx context.Context, b datastore.Backend, key string, v any) error {
	data, err := datastore.ReadAll(ctx, b, key, maxMeta)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("corrupt %s: %w", key, err)
	}
	return nil
}

func putJSON(ctx context.Context, b datastore.Backend, key string, v any, ifAbsent bool) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return datastore.PutBytes(ctx, b, key, data, datastore.PutOptions{IfAbsent: ifAbsent})
}

// ReadOwner returns owner.json, or nil when the repository has no owner yet.
func ReadOwner(ctx context.Context, b datastore.Backend) (*Owner, error) {
	var o Owner
	err := readJSON(ctx, b, ownerKey, &o)
	if errors.Is(err, datastore.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// ClaimOwner makes serverID the owner of b. An unowned repository is claimed
// with a create-only write (two nodes racing: exactly one wins). One owned by
// someone else is refused unless adopt is set, which replaces the owner and
// records who it was.
func ClaimOwner(ctx context.Context, b datastore.Backend, serverID, host string, adopt bool) error {
	for range 3 {
		cur, err := ReadOwner(ctx, b)
		if err != nil {
			return err
		}
		mine := Owner{ServerID: serverID, Host: host, Since: time.Now().UTC()}
		switch {
		case cur == nil:
			err := putJSON(ctx, b, ownerKey, mine, true)
			if errors.Is(err, datastore.ErrExists) {
				continue // lost a race: read who won
			}
			return err
		case cur.ServerID == serverID:
			return nil
		case adopt:
			mine.Previous = cur.ServerID
			return putJSON(ctx, b, ownerKey, mine, false)
		default:
			return fmt.Errorf("%w (owner %s since %s, host %q); use adopt only if that node is gone for good",
				ErrForeignOwner, cur.ServerID, cur.Since.Format(time.RFC3339), cur.Host)
		}
	}
	return errors.New("node: could not claim the repository (owner.json keeps changing)")
}
