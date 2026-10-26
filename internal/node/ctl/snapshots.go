package ctl

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"jailops.net/jailops/internal/node/wire"
)

// Snapshot is a committed snapshot as the node indexes it.
type Snapshot = wire.Snapshot

const snapCols = `group_id, snapshot_id, committed_at, chunk_count, index_bytes, index_digest, manifest_digest, source, protected,
	verify_state, verified_at, verify_problems, pending_deletion_requested_at, pending_deletion_after, pending_deletion_by`

func scanSnapshot(r scanner) (Snapshot, error) {
	var s Snapshot
	err := r.Scan(&s.GroupID, &s.SnapshotID, &s.CommittedAt, &s.ChunkCount, &s.IndexBytes, &s.IndexDigest, &s.ManifestDigest, &s.Source, &s.Protected,
		&s.VerifyState, &s.VerifiedAt, &s.VerifyProblems, &s.PendingDeletionRequestedAt, &s.PendingDeletionAfter, &s.PendingDeletionBy)
	return s, notFound(err)
}

// UpsertSnapshot records a snapshot (a commit or reconcile). The group row is created if missing.
func (d *DB) UpsertSnapshot(ctx context.Context, s Snapshot) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `INSERT INTO groups (group_id) VALUES ($1) ON CONFLICT DO NOTHING`, s.GroupID); err != nil {
		return err
	}
	if s.VerifyProblems == nil {
		s.VerifyProblems = []string{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO snapshots (`+snapCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (group_id, snapshot_id) DO UPDATE SET committed_at=EXCLUDED.committed_at, chunk_count=EXCLUDED.chunk_count,
			index_bytes=EXCLUDED.index_bytes, index_digest=EXCLUDED.index_digest, manifest_digest=EXCLUDED.manifest_digest, source=EXCLUDED.source,
			protected=EXCLUDED.protected, verify_state=EXCLUDED.verify_state, verified_at=EXCLUDED.verified_at, verify_problems=EXCLUDED.verify_problems,
			pending_deletion_requested_at=EXCLUDED.pending_deletion_requested_at, pending_deletion_after=EXCLUDED.pending_deletion_after,
			pending_deletion_by=EXCLUDED.pending_deletion_by`,
		s.GroupID, s.SnapshotID, s.CommittedAt, s.ChunkCount, s.IndexBytes, s.IndexDigest, s.ManifestDigest, s.Source, s.Protected,
		s.VerifyState, s.VerifiedAt, s.VerifyProblems, s.PendingDeletionRequestedAt, s.PendingDeletionAfter, s.PendingDeletionBy); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE groups SET last_activity = now() WHERE group_id = $1`, s.GroupID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Snapshot returns one snapshot.
func (d *DB) Snapshot(ctx context.Context, group, snap string) (Snapshot, error) {
	return scanSnapshot(d.pool.QueryRow(ctx, `SELECT `+snapCols+` FROM snapshots WHERE group_id=$1 AND snapshot_id=$2`, group, snap))
}

// Snapshots lists the snapshots of a group (all groups when group is empty), oldest first.
func (d *DB) Snapshots(ctx context.Context, group string) ([]Snapshot, error) {
	var rows pgx.Rows
	var err error
	if group == "" {
		rows, err = d.pool.Query(ctx, `SELECT `+snapCols+` FROM snapshots ORDER BY group_id, committed_at, snapshot_id`)
	} else {
		rows, err = d.pool.Query(ctx, `SELECT `+snapCols+` FROM snapshots WHERE group_id=$1 ORDER BY committed_at, snapshot_id`, group)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snapshot
	for rows.Next() {
		s, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSnapshotRow removes the index row of a snapshot.
func (d *DB) DeleteSnapshotRow(ctx context.Context, group, snap string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM snapshots WHERE group_id=$1 AND snapshot_id=$2`, group, snap)
	return err
}

// SetProtected records the protection flag.
func (d *DB) SetProtected(ctx context.Context, group, snap string, v bool) error {
	return d.mustChange(ctx, `UPDATE snapshots SET protected=$3 WHERE group_id=$1 AND snapshot_id=$2`, group, snap, v)
}

// SetVerify records a verification result.
func (d *DB) SetVerify(ctx context.Context, group, snap, state string, at time.Time, problems []string) error {
	if problems == nil {
		problems = []string{}
	}
	return d.mustChange(ctx, `UPDATE snapshots SET verify_state=$3, verified_at=$4, verify_problems=$5 WHERE group_id=$1 AND snapshot_id=$2`,
		group, snap, state, at, problems)
}

// SetPendingDeletion records or (with a nil time) clears a deletion request.
func (d *DB) SetPendingDeletion(ctx context.Context, group, snap string, requested, after *time.Time, by string) error {
	return d.mustChange(ctx, `UPDATE snapshots SET pending_deletion_requested_at=$3, pending_deletion_after=$4, pending_deletion_by=$5 WHERE group_id=$1 AND snapshot_id=$2`,
		group, snap, requested, after, by)
}

func (d *DB) mustChange(ctx context.Context, sql string, args ...any) error {
	ct, err := d.pool.Exec(ctx, sql, args...)
	if err == nil && ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ClearIndex drops every group and snapshot row (and the sessions that refer to
// groups). Reconcile rebuilds them from the repository.
func (d *DB) ClearIndex(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `TRUNCATE sessions, snapshots, groups`)
	return err
}

// Stats are the totals shown on the status page.
type Stats struct {
	Groups    int
	Snapshots int
	Failed    int
	Protected int
	Pending   int
}

// Stats counts the index.
func (d *DB) Stats(ctx context.Context) (Stats, error) {
	var s Stats
	err := d.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM groups), count(*), count(*) FILTER (WHERE verify_state='failed'),
		count(*) FILTER (WHERE protected), count(*) FILTER (WHERE pending_deletion_after IS NOT NULL) FROM snapshots`).Scan(&s.Groups, &s.Snapshots, &s.Failed, &s.Protected, &s.Pending)
	return s, err
}
