package ctl

import (
	"context"
	"time"
)

// User is a person who may use the management API.
type User struct {
	Name         string    `json:"name"`
	PasswordHash string    `json:"-"`
	Role         string    `json:"role"`
	Disabled     bool      `json:"disabled"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateUser adds a user.
func (d *DB) CreateUser(ctx context.Context, name, hash, role string) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO users (name, password_hash, role) VALUES ($1,$2,$3)`, name, hash, role)
	if isUnique(err) {
		return ErrConflict
	}
	return err
}

// User returns a user.
func (d *DB) User(ctx context.Context, name string) (User, error) {
	var u User
	err := d.pool.QueryRow(ctx, `SELECT name, password_hash, role, disabled, created_at FROM users WHERE name=$1`, name).
		Scan(&u.Name, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt)
	return u, notFound(err)
}

// Users lists the users.
func (d *DB) Users(ctx context.Context) ([]User, error) {
	rows, err := d.pool.Query(ctx, `SELECT name, password_hash, role, disabled, created_at FROM users ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.Name, &u.PasswordHash, &u.Role, &u.Disabled, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser changes the role, the disabled flag and, when non-empty, the password hash.
func (d *DB) UpdateUser(ctx context.Context, name, role string, disabled bool, hash string) error {
	return d.mustChange(ctx, `UPDATE users SET role=$2, disabled=$3, password_hash = CASE WHEN $4 = '' THEN password_hash ELSE $4 END WHERE name=$1`, name, role, disabled, hash)
}

// EndUserSessions ends the browser sessions of a user, except the one whose id hash is keep (nil keeps none).
func (d *DB) EndUserSessions(ctx context.Context, user string, keep []byte) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM ui_sessions WHERE user_name=$1 AND ($2::bytea IS NULL OR id_hash <> $2)`, user, keep)
	return err
}

// DeleteUser removes a user and, by cascade, their UI sessions.
func (d *DB) DeleteUser(ctx context.Context, name string) error {
	return d.mustChange(ctx, `DELETE FROM users WHERE name=$1`, name)
}

// CountAdmins counts enabled administrators.
func (d *DB) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := d.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE role='admin' AND NOT disabled`).Scan(&n)
	return n, err
}

// Token is an API token. Only its hash is stored.
type Token struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Hash       []byte     `json:"-"`
	Role       string     `json:"role"`
	AllowFrom  []string   `json:"allow_from,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
}

const tokCols = `id::text, name, hash, role, allow_from, expires_at, created_at, last_used_at`

func scanToken(r scanner) (Token, error) {
	var t Token
	err := r.Scan(&t.ID, &t.Name, &t.Hash, &t.Role, &t.AllowFrom, &t.ExpiresAt, &t.CreatedAt, &t.LastUsedAt)
	return t, notFound(err)
}

// CreateToken stores a token hash.
func (d *DB) CreateToken(ctx context.Context, name string, hash []byte, role string, allowFrom []string, expires *time.Time) (Token, error) {
	if allowFrom == nil {
		allowFrom = []string{}
	}
	t, err := scanToken(d.pool.QueryRow(ctx, `INSERT INTO tokens (name, hash, role, allow_from, expires_at) VALUES ($1,$2,$3,$4,$5) RETURNING `+tokCols,
		name, hash, role, allowFrom, expires))
	if isUnique(err) {
		return Token{}, ErrConflict
	}
	return t, err
}

// TokenByHash finds a token by the hash of its secret.
func (d *DB) TokenByHash(ctx context.Context, hash []byte) (Token, error) {
	return scanToken(d.pool.QueryRow(ctx, `SELECT `+tokCols+` FROM tokens WHERE hash=$1`, hash))
}

// TouchToken records a use.
func (d *DB) TouchToken(ctx context.Context, id string) {
	_, _ = d.pool.Exec(ctx, `UPDATE tokens SET last_used_at=now() WHERE id=$1::uuid AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')`, id)
}

// Tokens lists the tokens.
func (d *DB) Tokens(ctx context.Context) ([]Token, error) {
	rows, err := d.pool.Query(ctx, `SELECT `+tokCols+` FROM tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteToken removes a token by name.
func (d *DB) DeleteToken(ctx context.Context, name string) error {
	return d.mustChange(ctx, `DELETE FROM tokens WHERE name=$1`, name)
}

// UISession is a browser session.
type UISession struct {
	User      string
	CSRF      string
	ExpiresAt time.Time
}

// CreateUISession stores a session by the hash of its id.
func (d *DB) CreateUISession(ctx context.Context, idHash []byte, user, csrf string, ttl time.Duration) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO ui_sessions (id_hash, user_name, csrf, expires_at) VALUES ($1,$2,$3, now() + $4 * interval '1 microsecond')`,
		idHash, user, csrf, ttl.Microseconds())
	return err
}

// UISessionByHash returns a live session.
func (d *DB) UISessionByHash(ctx context.Context, idHash []byte) (UISession, error) {
	var s UISession
	err := d.pool.QueryRow(ctx, `SELECT user_name, csrf, expires_at FROM ui_sessions WHERE id_hash=$1 AND expires_at > now()`, idHash).Scan(&s.User, &s.CSRF, &s.ExpiresAt)
	return s, notFound(err)
}

// DeleteUISession ends a session.
func (d *DB) DeleteUISession(ctx context.Context, idHash []byte) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM ui_sessions WHERE id_hash=$1`, idHash)
	return err
}

// PurgeUISessions removes expired sessions.
func (d *DB) PurgeUISessions(ctx context.Context) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM ui_sessions WHERE expires_at <= now()`)
	return err
}
