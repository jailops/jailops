package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"jailops.net/jailops/sdk/api"
)

// Token is an API token record. Only the SHA-256 of the secret is stored.
type Token struct {
	ID         string
	Name       string
	Hash       []byte // sha256 of the full token string
	Permission api.Permission
	ExpiresAt  time.Time // zero: never
	AllowFrom  []string
	Projects   []string
	Global     bool
	CreatedAt  time.Time
	LastUsedAt time.Time
}

// Tokens is the token repository.
type Tokens struct{ s *Store }

const tokenCols = "id, name, hash, permission, expires_at, allow_from, created_at, last_used_at, projects, global"

func scanToken(r scanner) (*Token, error) {
	var t Token
	var perm, allow string
	var exp, c, lu int64
	var projs string
	var glob int
	if err := r.Scan(&t.ID, &t.Name, &t.Hash, &perm, &exp, &allow, &c, &lu, &projs, &glob); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(projs), &t.Projects); err != nil {
		return nil, err
	}
	t.Global = glob != 0
	t.Permission = api.Permission(perm)
	t.ExpiresAt, t.CreatedAt, t.LastUsedAt = fromTS(exp), fromTS(c), fromTS(lu)
	if err := json.Unmarshal([]byte(allow), &t.AllowFrom); err != nil {
		return nil, err
	}
	return &t, nil
}

// Create stores a token record. Hash must be 32 bytes.
func (r *Tokens) Create(ctx context.Context, t Token) error {
	if t.ID == "" || t.Name == "" || len(t.Hash) != 32 || t.Permission == "" {
		return api.Errorf(api.CodeInvalid, "token needs id, name, 32-byte hash and permission")
	}
	allow, _ := json.Marshal(nonNil(t.AllowFrom))
	projs, _ := json.Marshal(nonNil(t.Projects))
	return mapErr(r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO tokens ("+tokenCols+") VALUES (?,?,?,?,?,?,?,0,?,?)",
			t.ID, t.Name, t.Hash, string(t.Permission), ts(t.ExpiresAt), string(allow), ts(r.s.now()), string(projs), b2i(t.Global))
		return err
	}), "token "+t.Name)
}

// GetByHash looks a token up by the sha256 of its secret.
func (r *Tokens) GetByHash(ctx context.Context, hash []byte) (*Token, error) {
	t, err := scanToken(r.s.r.QueryRowContext(ctx, "SELECT "+tokenCols+" FROM tokens WHERE hash = ?", hash))
	if err != nil {
		return nil, mapErr(err, "token")
	}
	return t, nil
}

// Get returns a token by id.
func (r *Tokens) Get(ctx context.Context, id string) (*Token, error) {
	t, err := scanToken(r.s.r.QueryRowContext(ctx, "SELECT "+tokenCols+" FROM tokens WHERE id = ?", id))
	if err != nil {
		return nil, mapErr(err, "token "+id)
	}
	return t, nil
}

// List returns all tokens by name. The hash is included; callers must not serialise it.
func (r *Tokens) List(ctx context.Context) ([]*Token, error) {
	rows, err := r.s.r.QueryContext(ctx, "SELECT "+tokenCols+" FROM tokens ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TouchUsed records a use.
func (r *Tokens) TouchUsed(ctx context.Context, id string) error {
	return r.s.write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE tokens SET last_used_at = ? WHERE id = ?", ts(r.s.now()), id)
		return err
	})
}

// Delete revokes a token.
func (r *Tokens) Delete(ctx context.Context, id string) error {
	return r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM tokens WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return api.Errorf(api.CodeNotFound, "token %s not found", id)
		}
		return nil
	})
}

// DeleteExpired removes tokens past their expiry and returns how many.
func (r *Tokens) DeleteExpired(ctx context.Context) (int64, error) {
	var n int64
	err := r.s.write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM tokens WHERE expires_at != 0 AND expires_at <= ?", ts(r.s.now()))
		if err == nil {
			n, _ = res.RowsAffected()
		}
		return err
	})
	return n, err
}
