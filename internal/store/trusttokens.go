package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"jailops.net/jailops/sdk/api"
)

// ErrTrustTokenUnusable is returned when a trust token is unknown, already
// consumed or expired. The three cases are deliberately indistinguishable.
var ErrTrustTokenUnusable error = api.Errorf(api.CodeUnauthorized, "invalid trust token")

// TrustToken is the grant behind a single-use trust token. The token string
// itself is never stored.
type TrustToken struct {
	ID            string
	Name          string
	Permission    api.Permission
	Projects      []string
	Global        bool
	CertExpiresAt time.Time // expiry of the certificate trust it creates, zero: none
	ExpiresAt     time.Time // expiry of the token
	CreatedAt     time.Time
	CreatedBy     string
	ConsumedAt    time.Time
	ConsumedBy    string
}

// TrustTokens is the trust token repository.
type TrustTokens struct{ s *Store }

const trustCols = "id, name, permission, projects, global, cert_expires_at, expires_at, created_at, created_by, consumed_at, consumed_by"

func scanTrust(r scanner) (*TrustToken, error) {
	var t TrustToken
	var perm, projs string
	var glob int
	var ce, ex, cr, co int64
	if err := r.Scan(&t.ID, &t.Name, &perm, &projs, &glob, &ce, &ex, &cr, &t.CreatedBy, &co, &t.ConsumedBy); err != nil {
		return nil, err
	}
	t.Permission = api.Permission(perm)
	t.Global = glob != 0
	t.CertExpiresAt, t.ExpiresAt, t.CreatedAt, t.ConsumedAt = fromTS(ce), fromTS(ex), fromTS(cr), fromTS(co)
	if err := json.Unmarshal([]byte(projs), &t.Projects); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create stores a grant.
func (r *TrustTokens) Create(ctx context.Context, t TrustToken) error {
	if t.ID == "" || t.Name == "" || t.Permission == "" || t.ExpiresAt.IsZero() {
		return api.Errorf(api.CodeInvalid, "trust token needs id, name, permission and expiry")
	}
	if t.CreatedAt.IsZero() {
		t.CreatedAt = r.s.now()
	}
	projs, _ := json.Marshal(nonNil(t.Projects))
	return mapErr(r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO trust_tokens ("+trustCols+") VALUES (?,?,?,?,?,?,?,?,?,0,'')",
			t.ID, t.Name, string(t.Permission), string(projs), b2i(t.Global), ts(t.CertExpiresAt), ts(t.ExpiresAt), ts(t.CreatedAt), t.CreatedBy)
		return err
	}), "trust token "+t.Name)
}

// Get returns a grant by id.
func (r *TrustTokens) Get(ctx context.Context, id string) (*TrustToken, error) {
	t, err := scanTrust(r.s.r.QueryRowContext(ctx, "SELECT "+trustCols+" FROM trust_tokens WHERE id = ?", id))
	if err != nil {
		return nil, mapErr(err, "trust token")
	}
	return t, nil
}

// Consume marks the token used, atomically: of any number of concurrent
// calls exactly one succeeds. A missing, consumed or expired token returns
// ErrTrustTokenUnusable.
func (r *TrustTokens) Consume(ctx context.Context, id, by string, now time.Time) (*TrustToken, error) {
	var out *TrustToken
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE trust_tokens SET consumed_at = ?, consumed_by = ? WHERE id = ? AND consumed_at = 0 AND expires_at > ?",
			ts(now), by, id, ts(now))
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrTrustTokenUnusable
		}
		out, err = scanTrust(tx.QueryRowContext(ctx, "SELECT "+trustCols+" FROM trust_tokens WHERE id = ?", id))
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// List returns all grants, newest first.
func (r *TrustTokens) List(ctx context.Context) ([]*TrustToken, error) {
	rows, err := r.s.r.QueryContext(ctx, "SELECT "+trustCols+" FROM trust_tokens ORDER BY created_at DESC, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TrustToken
	for rows.Next() {
		t, err := scanTrust(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PruneExpired deletes grants that expired or were consumed before olderThan.
func (r *TrustTokens) PruneExpired(ctx context.Context, olderThan time.Time) (int64, error) {
	var n int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM trust_tokens WHERE expires_at < ? OR (consumed_at != 0 AND consumed_at < ?)", ts(olderThan), ts(olderThan))
		if err == nil {
			n, _ = res.RowsAffected()
		}
		return err
	})
	return n, err
}
