-- +goose Up
-- Toolsets (ADR 0023): per server and team, all tools (narrowed = 0, the
-- default) or only the tools listed in server_team_tools (narrowed = 1; an
-- empty list means none). A list goes with its server_teams row.
ALTER TABLE server_teams ADD COLUMN narrowed INTEGER NOT NULL DEFAULT 0;

CREATE TABLE server_team_tools (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    server_id TEXT NOT NULL,
    team      TEXT NOT NULL,
    name      TEXT NOT NULL,                -- upstream tool name
    PRIMARY KEY (org_id, server_id, team, name),
    FOREIGN KEY (org_id, server_id, team) REFERENCES server_teams (org_id, server_id, team) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE server_team_tools;
ALTER TABLE server_teams DROP COLUMN narrowed;
