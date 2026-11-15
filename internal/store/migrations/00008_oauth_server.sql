-- +goose Up
-- F06 (ADR 0022): Privo is the authorization server for its own gateway. An
-- AI client signs in with OAuth; a member allows it; the client then calls
-- /mcp as that member.

-- The secret the opaque tokens are signed with (HMAC), sealed with the org's
-- data key like every other secret. One per org, made at startup.
CREATE TABLE oauth_keys (
    org_id      TEXT NOT NULL PRIMARY KEY REFERENCES orgs(id),
    key_version INTEGER NOT NULL,
    secret      BLOB NOT NULL,
    created_at  TEXT NOT NULL
);

-- Clients that are accepted because of a row here: a metadata document an
-- administrator approved (client_id is its https URL), or a client that
-- registered itself while dynamic registration was on (client_id is made by
-- Privo). The built-in known clients have no row.
CREATE TABLE oauth_clients (
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    client_id     TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('approved', 'registered')),
    name          TEXT NOT NULL DEFAULT '',
    redirect_uris TEXT NOT NULL DEFAULT '[]',  -- JSON list; registered clients only
    created_at    TEXT NOT NULL,
    created_by    TEXT NOT NULL DEFAULT '',    -- the approving member's user id; '' for a registration
    authorized_at TEXT,                        -- the last time a member allowed it
    PRIMARY KEY (org_id, client_id)
);

-- A grant: one member allowed one client to use the gateway as them. id is
-- the id every code and token of the grant carries. The member's role is not
-- copied here: it is read when a token is verified. Ending a grant deletes
-- the row and its tokens; the audit keeps the history.
CREATE TABLE oauth_grants (
    org_id       TEXT NOT NULL REFERENCES orgs(id),
    id           TEXT NOT NULL,
    user_id      TEXT NOT NULL,
    client_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL DEFAULT '',
    client_kind  TEXT NOT NULL,      -- known | approved | document | registered
    scope        TEXT NOT NULL,
    created_at   TEXT NOT NULL,      -- consent
    expires_at   TEXT NOT NULL,      -- consent plus 90 days, whatever is refreshed
    last_used_at TEXT NOT NULL,      -- the last code exchange or refresh
    PRIMARY KEY (org_id, id)
);
CREATE INDEX oauth_grants_user ON oauth_grants (org_id, user_id);
CREATE INDEX oauth_grants_client ON oauth_grants (org_id, client_id);

-- Codes and tokens, by the signature part of the value the client holds; the
-- value itself is never stored. request is what the library needs to finish
-- the flow (scopes, redirect URI, code challenge, expiries), as JSON. A used
-- code and a rotated refresh token stay as inactive rows so a second use is
-- recognised and ends the grant.
CREATE TABLE oauth_tokens (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    kind       TEXT NOT NULL CHECK (kind IN ('code', 'pkce', 'access', 'refresh')),
    signature  TEXT NOT NULL,
    grant_id   TEXT NOT NULL,
    request    TEXT NOT NULL,
    active     INTEGER NOT NULL DEFAULT 1,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    PRIMARY KEY (org_id, kind, signature)
);
CREATE INDEX oauth_tokens_grant ON oauth_tokens (org_id, grant_id);
CREATE INDEX oauth_tokens_expiry ON oauth_tokens (org_id, expires_at);

-- oauth_states holds only connections in flight since 0.7.1 (ADR 0016
-- amendment 1). Its rows live ten minutes, so they are dropped here; the
-- three columns of sign-ins go, and one row per member and server becomes a
-- rule of the database, not only of the code.
DELETE FROM oauth_states;
ALTER TABLE oauth_states DROP COLUMN setup_link_id;
ALTER TABLE oauth_states DROP COLUMN invite_id;
ALTER TABLE oauth_states DROP COLUMN return_to;
CREATE UNIQUE INDEX oauth_states_member_server ON oauth_states (org_id, user_id, server_id);

-- +goose Down
DROP INDEX oauth_states_member_server;
ALTER TABLE oauth_states ADD COLUMN return_to     TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN invite_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE oauth_states ADD COLUMN setup_link_id TEXT NOT NULL DEFAULT '';
DROP TABLE oauth_tokens;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
DROP TABLE oauth_keys;
