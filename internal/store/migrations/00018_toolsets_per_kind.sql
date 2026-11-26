-- +goose Up
-- Toolsets per item kind (F08 next slice): prompt, resource and
-- resource_template lists alongside tool lists.
ALTER TABLE server_teams ADD COLUMN narrowed_prompts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_teams ADD COLUMN narrowed_resources INTEGER NOT NULL DEFAULT 0;
ALTER TABLE server_teams ADD COLUMN narrowed_templates INTEGER NOT NULL DEFAULT 0;

CREATE TABLE server_team_tools_new (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    server_id TEXT NOT NULL,
    team      TEXT NOT NULL,
    kind      TEXT NOT NULL DEFAULT 'tool',
    name      TEXT NOT NULL,
    PRIMARY KEY (org_id, server_id, team, kind, name),
    FOREIGN KEY (org_id, server_id, team) REFERENCES server_teams (org_id, server_id, team) ON DELETE CASCADE
);

INSERT INTO server_team_tools_new (org_id, server_id, team, kind, name)
SELECT org_id, server_id, team, 'tool', name FROM server_team_tools;

DROP TABLE server_team_tools;
ALTER TABLE server_team_tools_new RENAME TO server_team_tools;

-- +goose Down
CREATE TABLE server_team_tools_old (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    server_id TEXT NOT NULL,
    team      TEXT NOT NULL,
    name      TEXT NOT NULL,
    PRIMARY KEY (org_id, server_id, team, name),
    FOREIGN KEY (org_id, server_id, team) REFERENCES server_teams (org_id, server_id, team) ON DELETE CASCADE
);

INSERT INTO server_team_tools_old (org_id, server_id, team, name)
SELECT org_id, server_id, team, name FROM server_team_tools WHERE kind = 'tool';

DROP TABLE server_team_tools;
ALTER TABLE server_team_tools_old RENAME TO server_team_tools;

ALTER TABLE server_teams DROP COLUMN narrowed_templates;
ALTER TABLE server_teams DROP COLUMN narrowed_resources;
ALTER TABLE server_teams DROP COLUMN narrowed_prompts;
