-- Control plane of the backup node. The repository (directory or S3) is the
-- source of truth for payloads and for the relations between groups, snapshots
-- and chunks; `reconcile` rebuilds groups and snapshots from it. Users,
-- tokens, enrolments, policies and the audit log exist only here.

CREATE TABLE hosts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE CHECK (name <> ''),
    fingerprint  text NOT NULL UNIQUE CHECK (fingerprint ~ '^[0-9a-f]{64}$'),
    scopes       text[] NOT NULL,
    note         text NOT NULL DEFAULT '',
    source       text NOT NULL DEFAULT 'cli',
    created_at   timestamptz NOT NULL DEFAULT now(),
    revoked_at   timestamptz,
    last_seen_at timestamptz
);

-- A group is the backups of one jail. It belongs to the first host that backs it up.
CREATE TABLE groups (
    group_id      text PRIMARY KEY CHECK (group_id ~ '^[0-9a-f]{32}$'),
    host_id       uuid REFERENCES hosts(id),
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_activity timestamptz
);

-- A session is a lease on one group: the writer of the next snapshot.
CREATE TABLE sessions (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id         text NOT NULL REFERENCES groups(group_id),
    host_id          uuid NOT NULL REFERENCES hosts(id),
    snapshot_id      text NOT NULL,
    previous_id      text NOT NULL DEFAULT '',
    state            text NOT NULL CHECK (state IN ('open', 'committed', 'aborted', 'expired')),
    created_at       timestamptz NOT NULL DEFAULT now(),
    lease_expires_at timestamptz NOT NULL,
    closed_at        timestamptz,
    chunks_put       bigint NOT NULL DEFAULT 0,
    bytes_put        bigint NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX sessions_one_open_per_group ON sessions (group_id) WHERE state = 'open';
CREATE INDEX sessions_open_lease ON sessions (lease_expires_at) WHERE state = 'open';

-- Committed snapshots. Rebuilt from the repository by reconcile.
CREATE TABLE snapshots (
    group_id        text NOT NULL REFERENCES groups(group_id),
    snapshot_id     text NOT NULL,
    committed_at    timestamptz NOT NULL,
    chunk_count     integer NOT NULL,
    index_bytes     integer NOT NULL,
    index_digest    text NOT NULL,
    manifest_digest text NOT NULL,
    source          text NOT NULL,
    protected       boolean NOT NULL DEFAULT false,
    verify_state    text NOT NULL DEFAULT '' CHECK (verify_state IN ('', 'ok', 'failed')),
    verified_at     timestamptz,
    verify_problems text[] NOT NULL DEFAULT '{}',
    pending_deletion_requested_at timestamptz,
    pending_deletion_after        timestamptz,
    pending_deletion_by           text NOT NULL DEFAULT '',
    PRIMARY KEY (group_id, snapshot_id)
);
CREATE INDEX snapshots_committed ON snapshots (committed_at);

-- Jobs: garbage collection, verification, retention, reconcile, restore tests.
CREATE TABLE jobs (
    id          bigserial PRIMARY KEY,
    kind        text NOT NULL,
    state       text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    params      jsonb NOT NULL DEFAULT '{}',
    result      jsonb,
    error       text NOT NULL DEFAULT '',
    created_by  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    started_at  timestamptz,
    finished_at timestamptz
);
-- One destructive or repository-wide job at a time.
CREATE UNIQUE INDEX jobs_one_exclusive ON jobs ((true))
    WHERE state IN ('queued', 'running') AND kind IN ('gc', 'retention', 'verify', 'reconcile', 'quarantine');
CREATE INDEX jobs_recent ON jobs (id DESC);

-- Retention policies: scope is 'default' or a group id.
CREATE TABLE retention_policies (
    scope      text PRIMARY KEY,
    policy     jsonb NOT NULL,
    timezone   text NOT NULL DEFAULT 'UTC',
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key        text PRIMARY KEY,
    value      jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
    name          text PRIMARY KEY CHECK (name ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    password_hash text NOT NULL,
    role          text NOT NULL CHECK (role IN ('readonly', 'backup-admin', 'admin')),
    disabled      boolean NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name         text NOT NULL UNIQUE,
    hash         bytea NOT NULL UNIQUE,
    role         text NOT NULL CHECK (role IN ('readonly', 'backup-admin', 'admin')),
    allow_from   text[] NOT NULL DEFAULT '{}',
    expires_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz
);

CREATE TABLE ui_sessions (
    id_hash    bytea PRIMARY KEY,
    user_name  text NOT NULL REFERENCES users(name) ON DELETE CASCADE,
    csrf       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);

CREATE TABLE audit (
    id     bigserial PRIMARY KEY,
    at     timestamptz NOT NULL DEFAULT now(),
    actor  text NOT NULL,
    action text NOT NULL,
    target text NOT NULL DEFAULT '',
    detail jsonb NOT NULL DEFAULT '{}',
    remote text NOT NULL DEFAULT ''
);
CREATE INDEX audit_at ON audit (at DESC);
