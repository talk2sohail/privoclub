-- +goose Up
-- Web sign-in (ADR 0016): people, the identities that sign them in, browser
-- sessions, first-owner setup links and OAuth sign-ins in flight. Every table
-- carries org_id and every index starts with it (ADR 0002).
--
-- Secrets are stored only as SHA-256 hashes: sessions.id (the cookie secret),
-- setup_links.secret_hash and oauth_states.state_hash. A GitHub access token has
-- no column: it is used once and dropped.

CREATE TABLE users (
    org_id       TEXT NOT NULL REFERENCES orgs(id),
    id           TEXT NOT NULL,
    person       TEXT NOT NULL,   -- what self-approval compares with a token's person
    display_name TEXT NOT NULL,
    role         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    disabled_at  TEXT,
    PRIMARY KEY (org_id, id)
);
CREATE UNIQUE INDEX users_org_person ON users (org_id, person);

-- Keyed on provider plus the provider's stable subject (GitHub: the numeric id),
-- never on a login or an email. login is a display snapshot only.
CREATE TABLE identities (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    provider   TEXT NOT NULL,
    subject    TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    login      TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (org_id, provider, subject),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id)
);
CREATE INDEX identities_org_user ON identities (org_id, user_id);

CREATE TABLE sessions (
    org_id       TEXT NOT NULL REFERENCES orgs(id),
    id           TEXT NOT NULL,   -- hex SHA-256 of the cookie secret
    user_id      TEXT NOT NULL,
    csrf         TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    expires_at   TEXT NOT NULL,   -- absolute lifetime
    PRIMARY KEY (org_id, id),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id)
);
CREATE INDEX sessions_org_user ON sessions (org_id, user_id);

CREATE TABLE setup_links (
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    id          TEXT NOT NULL,
    secret_hash TEXT NOT NULL,
    person      TEXT NOT NULL DEFAULT '',   -- the person name the owner asked for; empty means the GitHub login
    created_by  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    retired_at  TEXT,                       -- a newer link replaced it
    claimed_at  TEXT,
    claimed_by  TEXT,                       -- user id
    PRIMARY KEY (org_id, id)
);
CREATE UNIQUE INDEX setup_links_hash ON setup_links (org_id, secret_hash);

-- A sign-in in flight. The row is deleted when it is used, so a replay finds nothing.
CREATE TABLE oauth_states (
    org_id        TEXT NOT NULL REFERENCES orgs(id),
    state_hash    TEXT NOT NULL,
    purpose       TEXT NOT NULL,            -- signin | setup
    setup_link_id TEXT NOT NULL DEFAULT '',
    verifier      TEXT NOT NULL,            -- PKCE code verifier
    created_at    TEXT NOT NULL,
    expires_at    TEXT NOT NULL,
    PRIMARY KEY (org_id, state_hash)
);
CREATE INDEX oauth_states_expiry ON oauth_states (org_id, expires_at);

-- +goose Down
DROP TABLE oauth_states;
DROP TABLE setup_links;
DROP TABLE sessions;
DROP TABLE identities;
DROP TABLE users;
