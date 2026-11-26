-- +goose Up
-- Cross-server data-flow tags (ADR 0032): servers and tools tagged as
-- untrusted_source or egress_sink. tool_name '' means server-wide.
CREATE TABLE flow_tags (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    server_id  TEXT NOT NULL,
    tool_name  TEXT NOT NULL DEFAULT '',
    tag        TEXT NOT NULL CHECK (tag IN ('untrusted_source', 'egress_sink')),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (org_id, server_id, tool_name, tag),
    FOREIGN KEY (org_id, server_id) REFERENCES servers (org_id, id)
);

-- +goose Down
DROP TABLE flow_tags;
