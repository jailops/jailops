package store

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"jailops.net/jailops/sdk/api"
)

// AuditEntry records one state-changing request: who, when, which route, which
// object and the result. Entries never contain secrets.
type AuditEntry struct {
	ID        int64
	Time      time.Time
	Actor     string // user name, token name or certificate fingerprint
	ActorKind string // "user", "token", "certificate", "unix"
	Method    string
	Route     string
	Object    string
	Result    string // "ok", "denied", "error"
	Status    int
	Remote    string
	Detail    string
	// Project is the project of the request; empty for host-level requests.
	Project string
}

// Audit is the append-only audit log. There is deliberately no update or
// delete method, and database triggers reject both.
type Audit struct{ s *Store }

// Append adds an entry and returns its id. A zero Time is replaced by now.
func (r *Audit) Append(ctx context.Context, e AuditEntry) (int64, error) {
	if e.Actor == "" || e.Method == "" || e.Route == "" || e.Result == "" {
		return 0, api.Errorf(api.CodeInvalid, "audit entry needs actor, method, route and result")
	}
	if e.Time.IsZero() {
		e.Time = r.s.now()
	}
	var id int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "INSERT INTO audit (ts, actor, actor_kind, method, route, object, result, status, remote, detail, project) VALUES (?,?,?,?,?,?,?,?,?,?,?)",
			ts(e.Time), e.Actor, e.ActorKind, e.Method, e.Route, e.Object, e.Result, e.Status, e.Remote, e.Detail, e.Project)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		return err
	})
	return id, err
}

// AuditFilter narrows List. Zero fields match everything.
type AuditFilter struct {
	Actor    string
	Since    time.Time
	BeforeID int64 // pagination: only entries with a smaller id
	Limit    int
	// ProjectFilter limits the result to entries of Projects; an empty list
	// then returns nothing.
	ProjectFilter bool
	Projects      []string
}

// List returns entries newest first.
func (r *Audit) List(ctx context.Context, f AuditFilter) ([]AuditEntry, error) {
	if f.Limit <= 0 || f.Limit > 10000 {
		f.Limit = 100
	}
	q := "SELECT id, ts, actor, actor_kind, method, route, object, result, status, remote, detail, project FROM audit WHERE 1=1"
	var args []any
	if f.Actor != "" {
		q += " AND actor = ?"
		args = append(args, f.Actor)
	}
	if !f.Since.IsZero() {
		q += " AND ts >= ?"
		args = append(args, ts(f.Since))
	}
	if f.BeforeID > 0 {
		q += " AND id < ?"
		args = append(args, f.BeforeID)
	}
	if f.ProjectFilter {
		if len(f.Projects) == 0 {
			return nil, nil
		}
		q += " AND project IN (?" + strings.Repeat(",?", len(f.Projects)-1) + ")"
		for _, p := range f.Projects {
			args = append(args, p)
		}
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := r.s.r.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var t int64
		if err := rows.Scan(&e.ID, &t, &e.Actor, &e.ActorKind, &e.Method, &e.Route, &e.Object, &e.Result, &e.Status, &e.Remote, &e.Detail, &e.Project); err != nil {
			return nil, err
		}
		e.Time = fromTS(t)
		out = append(out, e)
	}
	return out, rows.Err()
}
