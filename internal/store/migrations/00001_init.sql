-- +goose Up
-- Every table carries org_id and every index starts with it (ADR 0002).
-- Exceptions, each documented: api_tokens_id (global lookup by token id
-- before the org is known) and leases (process-wide).

CREATE TABLE orgs (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- ADR 0013: each DEK version records which provider and key wrap it.
CREATE TABLE org_keys (
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    version     INTEGER NOT NULL,
    provider    TEXT NOT NULL,
    key_ref     TEXT NOT NULL,
    wrapped_dek BLOB NOT NULL,
    created_at  TEXT NOT NULL,
    PRIMARY KEY (org_id, version)
);

CREATE TABLE api_tokens (
    org_id       TEXT NOT NULL REFERENCES orgs(id),
    id           TEXT NOT NULL,
    name         TEXT NOT NULL,
    role         TEXT NOT NULL,
    secret_hash  TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    revoked_at   TEXT,
    PRIMARY KEY (org_id, id)
);
CREATE UNIQUE INDEX api_tokens_id ON api_tokens (id);
CREATE UNIQUE INDEX api_tokens_org_name ON api_tokens (org_id, name) WHERE revoked_at IS NULL;

CREATE TABLE servers (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    id         TEXT NOT NULL,
    alias      TEXT NOT NULL,
    transport  TEXT NOT NULL,
    config     TEXT NOT NULL, -- JSON, never secrets
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (org_id, id)
);
CREATE UNIQUE INDEX servers_org_alias ON servers (org_id, alias);

CREATE TABLE credentials (
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    id          TEXT NOT NULL,
    server_id   TEXT NOT NULL,
    owner_id    TEXT NOT NULL DEFAULT '',
    purpose     TEXT NOT NULL,
    key_version INTEGER NOT NULL,
    row_version INTEGER NOT NULL,
    ciphertext  BLOB NOT NULL,
    PRIMARY KEY (org_id, id)
);
CREATE INDEX credentials_org_server ON credentials (org_id, server_id);

-- Per-item pins (ADR 0001, 0002): tools, prompts and resources.
CREATE TABLE pins (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    server_id  TEXT NOT NULL,
    kind       TEXT NOT NULL,   -- tool | prompt | resource
    name       TEXT NOT NULL,   -- upstream name
    hash       TEXT NOT NULL,   -- canonical hash of the definition
    definition TEXT NOT NULL,   -- JSON as seen upstream
    status     TEXT NOT NULL,   -- pending | accepted | blocked
    updated_at TEXT NOT NULL,
    PRIMARY KEY (org_id, server_id, kind, name, hash)
);

-- Audit (ADR 0006): one intent row before the call, one completion row after.
CREATE TABLE audit_events (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    id         TEXT NOT NULL,
    call_id    TEXT NOT NULL,
    phase      TEXT NOT NULL,   -- intent | completion | admin
    ts         TEXT NOT NULL,
    actor      TEXT NOT NULL,   -- display name at the time of the event
    actor_id   TEXT NOT NULL DEFAULT '', -- stable id (token id); names can be reused
    client     TEXT NOT NULL DEFAULT '',
    server     TEXT NOT NULL DEFAULT '',
    item       TEXT NOT NULL DEFAULT '',
    action     TEXT NOT NULL DEFAULT '',
    decision   TEXT NOT NULL DEFAULT '',
    outcome    TEXT NOT NULL DEFAULT '',
    latency_ms INTEGER NOT NULL DEFAULT 0,
    payload    BLOB,            -- encrypted arguments, only when recording is on
    PRIMARY KEY (org_id, id)
);
CREATE INDEX audit_events_org_ts ON audit_events (org_id, ts);
CREATE INDEX audit_events_org_call ON audit_events (org_id, call_id);

CREATE TABLE settings (
    org_id TEXT NOT NULL REFERENCES orgs(id),
    key    TEXT NOT NULL,
    value  TEXT NOT NULL,
    PRIMARY KEY (org_id, key)
);

-- leases is created by the store before migrations run (it guards them).

-- +goose Down
DROP TABLE settings;
DROP TABLE audit_events;
DROP TABLE pins;
DROP TABLE credentials;
DROP TABLE servers;
DROP TABLE api_tokens;
DROP TABLE org_keys;
DROP TABLE orgs;
