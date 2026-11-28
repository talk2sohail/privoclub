-- +goose Up
-- Member tool toggles (ADR 0026 amendment): per-user denylist of tools a
-- member chose to hide from their own AI clients. Strictly narrows what pins
-- and team toolsets grant, and never widens it.
CREATE TABLE user_disabled_tools (
    org_id    TEXT NOT NULL REFERENCES orgs(id),
    user_id   TEXT NOT NULL,
    server_id TEXT NOT NULL,
    name      TEXT NOT NULL,                -- upstream tool name
    PRIMARY KEY (org_id, user_id, server_id, name),
    FOREIGN KEY (org_id, user_id) REFERENCES users (org_id, id) ON DELETE CASCADE,
    FOREIGN KEY (org_id, server_id) REFERENCES servers (org_id, id) ON DELETE CASCADE
);

-- +goose Down
DROP TABLE user_disabled_tools;
