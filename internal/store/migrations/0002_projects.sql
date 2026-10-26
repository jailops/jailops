-- Identities carry a project scope (design 8.2). Rows that exist before this
-- migration were unrestricted, so they become global; new rows default to
-- neither projects nor global, which can do nothing.
ALTER TABLE users ADD COLUMN projects TEXT NOT NULL DEFAULT '[]';
ALTER TABLE users ADD COLUMN global INTEGER NOT NULL DEFAULT 0;
ALTER TABLE tokens ADD COLUMN projects TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tokens ADD COLUMN global INTEGER NOT NULL DEFAULT 0;
UPDATE users SET global = 1;
UPDATE tokens SET global = 1;

ALTER TABLE audit ADD COLUMN project TEXT NOT NULL DEFAULT '';
CREATE INDEX audit_project ON audit(project);

CREATE TABLE trust_tokens (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL,
    permission      TEXT NOT NULL,
    projects        TEXT NOT NULL DEFAULT '[]',
    global          INTEGER NOT NULL DEFAULT 0,
    cert_expires_at INTEGER NOT NULL DEFAULT 0,
    expires_at      INTEGER NOT NULL,
    created_at      INTEGER NOT NULL,
    created_by      TEXT NOT NULL DEFAULT '',
    consumed_at     INTEGER NOT NULL DEFAULT 0,
    consumed_by     TEXT NOT NULL DEFAULT ''
) STRICT;
