package ctl

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"jailops.net/jailops/internal/node/wire"
)

// Host is an enrolled backup client: a pinned certificate and its scopes.
type Host struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Fingerprint string     `json:"fingerprint"`
	Scopes      []string   `json:"scopes"`
	Note        string     `json:"note,omitempty"`
	Source      string     `json:"source"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	LastSeenAt  *time.Time `json:"last_seen_at,omitempty"`
}

// Active reports whether the host may connect.
func (h Host) Active() bool { return h.RevokedAt == nil }

// HasScope reports whether the host holds the scope.
func (h Host) HasScope(s string) bool { return slices.Contains(h.Scopes, s) }

const hostCols = `id::text, name, fingerprint, scopes, note, source, created_at, revoked_at, last_seen_at`

func scanHost(r pgx.Row) (Host, error) {
	var h Host
	err := r.Scan(&h.ID, &h.Name, &h.Fingerprint, &h.Scopes, &h.Note, &h.Source, &h.CreatedAt, &h.RevokedAt, &h.LastSeenAt)
	return h, notFound(err)
}

// EnrollHost adds a host. The fingerprint and the name must be new.
func (d *DB) EnrollHost(ctx context.Context, name, fingerprint string, scopes []string, note, source string) (Host, error) {
	if source == "" {
		source = "cli"
	}
	if scopes == nil {
		scopes = []string{}
	}
	h, err := scanHost(d.pool.QueryRow(ctx, `INSERT INTO hosts (name, fingerprint, scopes, note, source) VALUES ($1,$2,$3,$4,$5) RETURNING `+hostCols,
		name, fingerprint, scopes, note, source))
	if isUnique(err) {
		return Host{}, fmt.Errorf("%w: a host with this name or fingerprint is already enrolled", ErrConflict)
	}
	return h, err
}

// EnsureHost enrols a host from static configuration unless its fingerprint is
// known already (a revoked host is never resurrected by a restart).
func (d *DB) EnsureHost(ctx context.Context, name, fingerprint string, scopes []string, note string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO hosts (name, fingerprint, scopes, note, source) VALUES ($1,$2,$3,$4,'config')
		ON CONFLICT DO NOTHING`, name, fingerprint, scopes, note)
	return err
}

// HostByFingerprint finds an enrolled host, revoked or not.
func (d *DB) HostByFingerprint(ctx context.Context, fp string) (Host, error) {
	return scanHost(d.pool.QueryRow(ctx, `SELECT `+hostCols+` FROM hosts WHERE fingerprint = $1`, fp))
}

// HostByID finds a host.
func (d *DB) HostByID(ctx context.Context, id string) (Host, error) {
	return scanHost(d.pool.QueryRow(ctx, `SELECT `+hostCols+` FROM hosts WHERE id = $1::uuid`, id))
}

// HostByName finds a host.
func (d *DB) HostByName(ctx context.Context, name string) (Host, error) {
	return scanHost(d.pool.QueryRow(ctx, `SELECT `+hostCols+` FROM hosts WHERE name = $1`, name))
}

// Hosts lists every host, oldest first.
func (d *DB) Hosts(ctx context.Context) ([]Host, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY created_at, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// RevokeHost stops a host from connecting. Its open sessions are aborted.
func (d *DB) RevokeHost(ctx context.Context, name string) (Host, error) {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return Host{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	h, err := scanHost(tx.QueryRow(ctx, `UPDATE hosts SET revoked_at = COALESCE(revoked_at, now()) WHERE name = $1 RETURNING `+hostCols, name))
	if err != nil {
		return Host{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE sessions SET state='aborted', closed_at=now() WHERE host_id=$1::uuid AND state='open'`, h.ID); err != nil {
		return Host{}, err
	}
	return h, tx.Commit(ctx)
}

// SetHostScopes replaces a host's scopes.
func (d *DB) SetHostScopes(ctx context.Context, name string, scopes []string) error {
	ct, err := d.pool.Exec(ctx, `UPDATE hosts SET scopes = $2 WHERE name = $1`, name, scopes)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// TouchHost records activity (at most once a minute per host).
func (d *DB) TouchHost(ctx context.Context, id string) {
	_, _ = d.pool.Exec(ctx, `UPDATE hosts SET last_seen_at = now() WHERE id = $1::uuid AND (last_seen_at IS NULL OR last_seen_at < now() - interval '1 minute')`, id)
}

// ---- groups ------------------------------------------------------------------

// Group is the backups of one jail.
type Group = wire.Group

// ErrGroupOwned is returned when another host owns the group.
var ErrGroupOwned = errors.New("ctl: the group belongs to another host")

// ClaimGroup makes host the owner of the group if it has none, and creates the
// group if needed. Another host's group is refused.
func (d *DB) ClaimGroup(ctx context.Context, group, hostID string) error {
	var owner *string
	err := d.pool.QueryRow(ctx, `
		INSERT INTO groups (group_id, host_id) VALUES ($1, $2::uuid)
		ON CONFLICT (group_id) DO UPDATE SET host_id = COALESCE(groups.host_id, EXCLUDED.host_id)
		RETURNING host_id::text`, group, hostID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner == nil || *owner != hostID {
		return ErrGroupOwned
	}
	return nil
}

// Groups lists the groups with their owners.
func (d *DB) Groups(ctx context.Context) ([]Group, error) {
	rows, err := d.pool.Query(ctx, `SELECT g.group_id, COALESCE(g.host_id::text,''), COALESCE(h.name,''), g.created_at, g.last_activity
		FROM groups g LEFT JOIN hosts h ON h.id = g.host_id ORDER BY g.group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.GroupID, &g.HostID, &g.HostName, &g.CreatedAt, &g.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GroupOwner returns the owner host id of a group ("" if unowned).
func (d *DB) GroupOwner(ctx context.Context, group string) (string, error) {
	var o *string
	err := d.pool.QueryRow(ctx, `SELECT host_id::text FROM groups WHERE group_id = $1`, group).Scan(&o)
	if err != nil {
		return "", notFound(err)
	}
	if o == nil {
		return "", nil
	}
	return *o, nil
}

// SetGroupOwner records the owner of an existing group (reconcile, from the owner marker in the repository).
func (d *DB) SetGroupOwner(ctx context.Context, group, hostID string) error {
	_, err := d.pool.Exec(ctx, `UPDATE groups SET host_id = $2::uuid WHERE group_id = $1`, group, hostID)
	return err
}
