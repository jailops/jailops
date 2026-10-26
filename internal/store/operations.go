package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Operations persists api.Operation so a restart can resume or roll back.
type Operations struct{ s *Store }

// Put inserts or replaces an operation.
func (r *Operations) Put(ctx context.Context, op api.Operation) error {
	if op.ID == "" {
		return api.Errorf(api.CodeInvalid, "operation needs an id")
	}
	doc, err := json.Marshal(op)
	if err != nil {
		return err
	}
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO operations (id, status, doc, interrupted, created_at, updated_at) VALUES (?,?,?,0,?,?)
			ON CONFLICT(id) DO UPDATE SET status = excluded.status, doc = excluded.doc, updated_at = excluded.updated_at`,
			op.ID, string(op.Status), string(doc), ts(op.CreatedAt), ts(op.UpdatedAt))
		return err
	})
}

func decodeOp(doc string) (*api.Operation, error) {
	var op api.Operation
	if err := json.Unmarshal([]byte(doc), &op); err != nil {
		return nil, err
	}
	return &op, nil
}

// Get returns one operation.
func (r *Operations) Get(ctx context.Context, id string) (*api.Operation, error) {
	var doc string
	if err := r.s.r.QueryRowContext(ctx, "SELECT doc FROM operations WHERE id = ?", id).Scan(&doc); err != nil {
		return nil, mapErr(err, "operation "+id)
	}
	return decodeOp(doc)
}

// List returns operations, newest first. An empty status matches all.
func (r *Operations) List(ctx context.Context, status api.OperationStatus, limit int) ([]*api.Operation, error) {
	if limit <= 0 {
		limit = 1000
	}
	q, args := "SELECT doc FROM operations ", []any{}
	if status != "" {
		q += "WHERE status = ? "
		args = append(args, string(status))
	}
	q += "ORDER BY created_at DESC, id LIMIT ?"
	args = append(args, limit)
	rows, err := r.s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Operation
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		op, err := decodeOp(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}

// MarkInterrupted is called at daemon start. Operations still pending or
// running belong to a dead process: each becomes failed with an
// "interrupted" error, is flagged, and is returned so the owner can resume or
// roll back.
func (r *Operations) MarkInterrupted(ctx context.Context) ([]*api.Operation, error) {
	var out []*api.Operation
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT doc FROM operations WHERE status IN (?, ?)", string(api.OpPending), string(api.OpRunning))
		if err != nil {
			return err
		}
		var ops []*api.Operation
		for rows.Next() {
			var doc string
			if err := rows.Scan(&doc); err != nil {
				rows.Close()
				return err
			}
			op, err := decodeOp(doc)
			if err != nil {
				rows.Close()
				return err
			}
			ops = append(ops, op)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		now := r.s.now().UTC()
		for _, op := range ops {
			op.Status = api.OpFailure
			op.MayCancel = false
			op.Error = api.Errorf(api.CodeInternal, "operation interrupted by daemon restart")
			op.UpdatedAt = now
			doc, _ := json.Marshal(op)
			if _, err := tx.ExecContext(ctx, "UPDATE operations SET status = ?, doc = ?, interrupted = 1, updated_at = ? WHERE id = ?",
				string(op.Status), string(doc), ts(now), op.ID); err != nil {
				return err
			}
		}
		out = ops
		return nil
	})
	return out, err
}

// WasInterrupted reports whether MarkInterrupted flagged the operation.
func (r *Operations) WasInterrupted(ctx context.Context, id string) (bool, error) {
	var v int
	if err := r.s.r.QueryRowContext(ctx, "SELECT interrupted FROM operations WHERE id = ?", id).Scan(&v); err != nil {
		return false, mapErr(err, "operation "+id)
	}
	return v != 0, nil
}

// Prune deletes finished operations last updated before cutoff.
func (r *Operations) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM operations WHERE status IN (?,?,?) AND updated_at < ?",
			string(api.OpSuccess), string(api.OpFailure), string(api.OpCancelled), ts(cutoff))
		if err == nil {
			n, _ = res.RowsAffected()
		}
		return err
	})
	return n, err
}

// ListForProjects returns operations newest first. With all set every
// operation is returned; otherwise only those whose project is in projects
// (an empty list returns none, and operations without a project never match).
func (r *Operations) ListForProjects(ctx context.Context, all bool, projects []string, limit int) ([]*api.Operation, error) {
	if limit <= 0 {
		limit = 1000
	}
	q, args := "SELECT doc FROM operations ", []any{}
	if !all {
		if len(projects) == 0 {
			return nil, nil
		}
		q += "WHERE json_extract(doc, '$.project') IN (?" + strings.Repeat(",?", len(projects)-1) + ") "
		for _, p := range projects {
			args = append(args, p)
		}
	}
	q += "ORDER BY created_at DESC, id LIMIT ?"
	args = append(args, limit)
	rows, err := r.s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*api.Operation
	for rows.Next() {
		var doc string
		if err := rows.Scan(&doc); err != nil {
			return nil, err
		}
		op, err := decodeOp(doc)
		if err != nil {
			return nil, err
		}
		out = append(out, op)
	}
	return out, rows.Err()
}
