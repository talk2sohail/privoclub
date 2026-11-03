-- +goose Up
-- Teams and which servers they reach (ADR 0020). Every table carries org_id
-- and every index starts with it (ADR 0002).
--
-- The built-in team 'everyone' has no row: every active member is in it. It
-- can appear in server_teams, which is why server_teams.team has no foreign
-- key; deleting a team deletes its server_teams rows in the same transaction.

CREATE TABLE teams (
    org_id      TEXT NOT NULL REFERENCES orgs(id),
    slug        TEXT NOT NULL,              -- the key; never changes; rules name it (team:<slug>)
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    profile     TEXT NOT NULL DEFAULT '',   -- developers | designers | product | '' (catalog profiles)
    created_at  TEXT NOT NULL,
    PRIMARY KEY (org_id, slug)
);

CREATE TABLE team_members (
    org_id   TEXT NOT NULL REFERENCES orgs(id),
    team     TEXT NOT NULL,
    user_id  TEXT NOT NULL,
    added_at TEXT NOT NULL,
    PRIMARY KEY (org_id, team, user_id),
    FOREIGN KEY (org_id, team) REFERENCES teams (org_id, slug),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id)
);
CREATE INDEX team_members_org_user ON team_members (org_id, user_id);

CREATE TABLE server_teams (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    server_id TEXT NOT NULL,
    team      TEXT NOT NULL,                -- a teams.slug or 'everyone'
    PRIMARY KEY (org_id, server_id, team),
    FOREIGN KEY (org_id, server_id) REFERENCES servers (org_id, id)
);
CREATE INDEX server_teams_org_team ON server_teams (org_id, team);

-- Every server that exists today stays reachable by everyone.
INSERT INTO server_teams (org_id, server_id, team) SELECT org_id, id, 'everyone' FROM servers;

-- +goose Down
DROP TABLE server_teams;
DROP TABLE team_members;
DROP TABLE teams;
