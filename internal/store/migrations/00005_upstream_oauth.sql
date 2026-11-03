-- +goose Up
-- Invites and per-person upstream OAuth grants (ADR 0017). Every table carries
-- org_id and every index starts with it (ADR 0002).
--
-- Secrets: invites.secret_hash is a SHA-256 hash; upstream tokens and client
-- secrets live encrypted in credentials, never here.

CREATE TABLE invites (
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    id          TEXT NOT NULL,
    secret_hash TEXT NOT NULL,
    person      TEXT NOT NULL,
    role        TEXT NOT NULL,
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    retired_at  TEXT,               -- a newer invite for the same person replaced it
    claimed_at  TEXT,
    claimed_by  TEXT,               -- user id
    PRIMARY KEY (org_id, id)
);
CREATE UNIQUE INDEX invites_hash ON invites (org_id, secret_hash);
CREATE INDEX invites_person ON invites (org_id, person);

-- Sign-ins for invites and upstream connections in flight.
ALTER TABLE oauth_states ADD COLUMN invite_id  TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN user_id    TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN session_id TEXT NOT NULL DEFAULT '';  -- sessions.id (already a hash) that started a connection
ALTER TABLE oauth_states ADD COLUMN server_id  TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN issuer     TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN return_to  TEXT NOT NULL DEFAULT '';  -- a fixed local path to go back to after sign-in

-- One grant per server and person. The tokens are the credential named here
-- (owner_id = user_id, purpose = 'oauth'). catalog marks the grant the
-- server's tools are listed with; at most one per server.
CREATE TABLE upstream_grants (
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    server_id     TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    credential_id TEXT NOT NULL,
    scopes        TEXT NOT NULL DEFAULT '',
    catalog       INTEGER NOT NULL DEFAULT 0,
    connected_at  TEXT NOT NULL,
    last_used_at  TEXT NOT NULL,
    refreshed_at  TEXT NOT NULL,
    PRIMARY KEY (org_id, server_id, user_id),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id)
);
CREATE UNIQUE INDEX upstream_grants_catalog ON upstream_grants (org_id, server_id) WHERE catalog = 1;
CREATE INDEX upstream_grants_user ON upstream_grants (org_id, user_id);

-- +goose Down
DROP TABLE upstream_grants;
ALTER TABLE oauth_states DROP COLUMN return_to;
ALTER TABLE oauth_states DROP COLUMN issuer;
ALTER TABLE oauth_states DROP COLUMN server_id;
ALTER TABLE oauth_states DROP COLUMN session_id;
ALTER TABLE oauth_states DROP COLUMN user_id;
ALTER TABLE oauth_states DROP COLUMN invite_id;
DROP TABLE invites;
