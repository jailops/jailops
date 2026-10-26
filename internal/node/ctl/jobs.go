package ctl

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Job kinds.
const (
	JobGC          = "gc"
	JobVerify      = "verify"
	JobRetention   = "retention"
	JobReconcile   = "reconcile"
	JobQuarantine  = "quarantine"
	JobRestoreTest = "restore-test"
)

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCancelled = "cancelled"
)

// Job is a repository-wide task and its result.
type Job struct {
	ID         int64           `json:"id"`
	Kind       string          `json:"kind"`
	State      string          `json:"state"`
	Params     json.RawMessage `json:"params,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
	CreatedBy  string          `json:"created_by,omitempty"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  *time.Time      `json:"started_at,omitempty"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

const jobCols = `id, kind, state, params, result, error, created_by, created_at, started_at, finished_at`

func scanJob(r scanner) (Job, error) {
	var j Job
	var params, result []byte
	err := r.Scan(&j.ID, &j.Kind, &j.State, &params, &result, &j.Error, &j.CreatedBy, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	j.Params, j.Result = params, result
	return j, notFound(err)
}

// StartJob creates a running job. Repository-wide kinds are exclusive: a second
// one while another runs is ErrConflict.
func (d *DB) StartJob(ctx context.Context, kind, by string, params any) (Job, error) {
	pb, err := json.Marshal(params)
	if err != nil {
		return Job{}, err
	}
	if params == nil {
		pb = []byte("{}")
	}
	j, err := scanJob(d.pool.QueryRow(ctx, `INSERT INTO jobs (kind, state, params, created_by, started_at) VALUES ($1,'running',$2,$3,now()) RETURNING `+jobCols, kind, pb, by))
	if isUnique(err) {
		return Job{}, fmt.Errorf("%w: another repository-wide job is running", ErrConflict)
	}
	return j, err
}

// FinishJob records the outcome of a job.
func (d *DB) FinishJob(ctx context.Context, id int64, state string, result any, jobErr error) error {
	rb, err := json.Marshal(result)
	if err != nil {
		return err
	}
	if result == nil {
		rb = nil
	}
	msg := ""
	if jobErr != nil {
		msg = jobErr.Error()
	}
	_, err = d.pool.Exec(ctx, `UPDATE jobs SET state=$2, result=$3, error=$4, finished_at=now() WHERE id=$1`, id, state, rb, msg)
	return err
}

// Job returns a job.
func (d *DB) Job(ctx context.Context, id int64) (Job, error) {
	return scanJob(d.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE id=$1`, id))
}

// Jobs lists recent jobs, newest first; kind "" lists all kinds.
func (d *DB) Jobs(ctx context.Context, kind string, limit int) ([]Job, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := d.pool.Query(ctx, `SELECT `+jobCols+` FROM jobs WHERE ($1 = '' OR kind = $1) ORDER BY id DESC LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// LastJob returns the newest finished job of a kind.
func (d *DB) LastJob(ctx context.Context, kind string) (Job, error) {
	return scanJob(d.pool.QueryRow(ctx, `SELECT `+jobCols+` FROM jobs WHERE kind=$1 AND state IN ('succeeded','failed') ORDER BY id DESC LIMIT 1`, kind))
}

// AbandonJobs marks jobs that were running when the process died as failed.
func (d *DB) AbandonJobs(ctx context.Context) (int64, error) {
	ct, err := d.pool.Exec(ctx, `UPDATE jobs SET state='failed', error='the node stopped while the job was running', finished_at=now() WHERE state IN ('queued','running')`)
	return ct.RowsAffected(), err
}

// ---- retention policies --------------------------------------------------------

// RetentionPolicyRow is a stored retention policy.
type RetentionPolicyRow struct {
	Scope     string          `json:"scope"` // "default" or a group id
	Policy    json.RawMessage `json:"policy"`
	Timezone  string          `json:"timezone"`
	UpdatedAt time.Time       `json:"updated_at"`
	UpdatedBy string          `json:"updated_by"`
}

// SetRetentionPolicy stores a policy.
func (d *DB) SetRetentionPolicy(ctx context.Context, scope string, policy any, tz, by string) error {
	pb, err := json.Marshal(policy)
	if err != nil {
		return err
	}
	if tz == "" {
		tz = "UTC"
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO retention_policies (scope, policy, timezone, updated_by) VALUES ($1,$2,$3,$4)
		ON CONFLICT (scope) DO UPDATE SET policy=EXCLUDED.policy, timezone=EXCLUDED.timezone, updated_at=now(), updated_by=EXCLUDED.updated_by`, scope, pb, tz, by)
	return err
}

// DeleteRetentionPolicy removes a policy.
func (d *DB) DeleteRetentionPolicy(ctx context.Context, scope string) error {
	return d.mustChange(ctx, `DELETE FROM retention_policies WHERE scope=$1`, scope)
}

// RetentionPolicies lists the stored policies.
func (d *DB) RetentionPolicies(ctx context.Context) ([]RetentionPolicyRow, error) {
	rows, err := d.pool.Query(ctx, `SELECT scope, policy, timezone, updated_at, updated_by FROM retention_policies ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RetentionPolicyRow
	for rows.Next() {
		var r RetentionPolicyRow
		var p []byte
		if err := rows.Scan(&r.Scope, &p, &r.Timezone, &r.UpdatedAt, &r.UpdatedBy); err != nil {
			return nil, err
		}
		r.Policy = p
		out = append(out, r)
	}
	return out, rows.Err()
}

// ---- settings --------------------------------------------------------------------

// SetSetting stores a value.
func (d *DB) SetSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO settings (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, b)
	return err
}

// Setting reads a value into out. ErrNotFound when unset.
func (d *DB) Setting(ctx context.Context, key string, out any) error {
	var b []byte
	if err := d.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, key).Scan(&b); err != nil {
		return notFound(err)
	}
	return json.Unmarshal(b, out)
}

// ---- audit ----------------------------------------------------------------------------

// AuditEntry is one line of the audit log.
type AuditEntry struct {
	ID     int64           `json:"id"`
	At     time.Time       `json:"at"`
	Actor  string          `json:"actor"`
	Action string          `json:"action"`
	Target string          `json:"target,omitempty"`
	Detail json.RawMessage `json:"detail,omitempty"`
	Remote string          `json:"remote,omitempty"`
}

// Audit appends to the audit log.
func (d *DB) Audit(ctx context.Context, actor, action, target string, detail any, remote string) error {
	b, err := json.Marshal(detail)
	if err != nil || detail == nil {
		b = []byte("{}")
	}
	_, err = d.pool.Exec(ctx, `INSERT INTO audit (actor, action, target, detail, remote) VALUES ($1,$2,$3,$4,$5)`, actor, action, target, b, remote)
	return err
}

// AuditLog returns the newest entries first, optionally only those of one action prefix.
func (d *DB) AuditLog(ctx context.Context, actionPrefix string, limit int, beforeID int64) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	if beforeID <= 0 {
		beforeID = 1 << 62
	}
	rows, err := d.pool.Query(ctx, `SELECT id, at, actor, action, target, detail, remote FROM audit
		WHERE id < $3 AND action LIKE $1 || '%' ORDER BY id DESC LIMIT $2`, actionPrefix, limit, beforeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var det []byte
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &det, &e.Remote); err != nil {
			return nil, err
		}
		e.Detail = det
		out = append(out, e)
	}
	return out, rows.Err()
}
