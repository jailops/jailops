CREATE TABLE users (
    id                 TEXT PRIMARY KEY,
    name               TEXT NOT NULL COLLATE NOCASE UNIQUE,
    password_hash      TEXT NOT NULL,
    permission         TEXT NOT NULL,
    totp_sealed        BLOB,
    totp_enabled       INTEGER NOT NULL DEFAULT 0,
    recovery_hashes    TEXT NOT NULL DEFAULT '[]',
    disabled           INTEGER NOT NULL DEFAULT 0,
    created_at         INTEGER NOT NULL,
    updated_at         INTEGER NOT NULL,
    last_login_at      INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE tokens (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL UNIQUE,
    hash         BLOB NOT NULL UNIQUE CHECK (length(hash) = 32),
    permission   TEXT NOT NULL,
    expires_at   INTEGER NOT NULL DEFAULT 0,
    allow_from   TEXT NOT NULL DEFAULT '[]',
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE sessions (
    id_hash      BLOB PRIMARY KEY CHECK (length(id_hash) = 32),
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf         TEXT NOT NULL,
    remote       TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen    INTEGER NOT NULL,
    idle_expires INTEGER NOT NULL,
    max_expires  INTEGER NOT NULL
) STRICT;
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE operations (
    id          TEXT PRIMARY KEY,
    status      TEXT NOT NULL,
    doc         TEXT NOT NULL,
    interrupted INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX operations_status ON operations(status);

CREATE TABLE events (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       INTEGER NOT NULL,
    type     TEXT NOT NULL,
    host     TEXT NOT NULL DEFAULT '',
    metadata TEXT NOT NULL
) STRICT;
CREATE INDEX events_ts ON events(ts);

CREATE TABLE audit (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         INTEGER NOT NULL,
    actor      TEXT NOT NULL,
    actor_kind TEXT NOT NULL DEFAULT '',
    method     TEXT NOT NULL,
    route      TEXT NOT NULL,
    object     TEXT NOT NULL DEFAULT '',
    result     TEXT NOT NULL,
    status     INTEGER NOT NULL DEFAULT 0,
    remote     TEXT NOT NULL DEFAULT '',
    detail     TEXT NOT NULL DEFAULT ''
) STRICT;
CREATE INDEX audit_ts ON audit(ts);
CREATE INDEX audit_actor ON audit(actor);

-- The audit log is append-only at the database level too.
CREATE TRIGGER audit_no_update BEFORE UPDATE ON audit
BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;
CREATE TRIGGER audit_no_delete BEFORE DELETE ON audit
BEGIN SELECT RAISE(ABORT, 'audit log is append-only'); END;

CREATE TABLE meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
