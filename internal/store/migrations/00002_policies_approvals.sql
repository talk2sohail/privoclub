-- +goose Up
-- Access rules (ADR 0001 decision stage, ADR 0011 roles).
CREATE TABLE policies (
    org_id     TEXT NOT NULL REFERENCES orgs(id),
    id         TEXT NOT NULL,
    server     TEXT NOT NULL,              -- server alias or '*'
    tool       TEXT NOT NULL,              -- upstream tool name glob
    subject    TEXT NOT NULL,              -- '*', 'role:<role>' or 'token:<id>'
    action     TEXT NOT NULL,              -- allow | ask | deny
    priority   INTEGER NOT NULL DEFAULT 0, -- higher wins; ties: deny > ask > allow
    note       TEXT NOT NULL DEFAULT '',
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT,                       -- scoped temporary grants
    PRIMARY KEY (org_id, id)
);
CREATE INDEX policies_org_server ON policies (org_id, server);

-- Approvals (ADR 0005): durable, single use, bound to the exact arguments.
CREATE TABLE approvals (
    org_id         TEXT NOT NULL REFERENCES orgs(id),
    id             TEXT NOT NULL,
    requester_id   TEXT NOT NULL,
    requester_name TEXT NOT NULL,
    server_id      TEXT NOT NULL,
    server_alias   TEXT NOT NULL,
    tool           TEXT NOT NULL,
    args_digest    TEXT NOT NULL,
    args           BLOB NOT NULL,          -- encrypted full arguments, shown to approvers
    reason         TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL,          -- pending | approved | denied | withdrawn | consumed
    decided_by     TEXT NOT NULL DEFAULT '',
    decided_at     TEXT,
    created_at     TEXT NOT NULL,
    expires_at     TEXT NOT NULL,
    consumed_at    TEXT,
    PRIMARY KEY (org_id, id)
);
CREATE INDEX approvals_org_status ON approvals (org_id, status, created_at);
CREATE INDEX approvals_org_binding ON approvals (org_id, requester_id, server_id, tool, args_digest);

-- +goose Down
DROP TABLE approvals;
DROP TABLE policies;
