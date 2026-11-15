-- +goose Up
-- F28, ADR 0024: how calls went, apart from the audit (who did what). Both
-- tables are best effort and pruned after PRIVO_TELEMETRY_RETENTION_DAYS. Times
-- are Unix milliseconds so ranges and buckets are integer comparisons.
-- Nothing here holds arguments, results, credentials or error texts.
CREATE TABLE call_stats (
    org_id       TEXT    NOT NULL REFERENCES orgs(id),
    id           TEXT    NOT NULL,           -- the call id the audit uses
    ts_ms        INTEGER NOT NULL,           -- when the call arrived
    user_id      TEXT    NOT NULL DEFAULT '', -- the member the call ran as
    token_id     TEXT    NOT NULL DEFAULT '',
    client       TEXT    NOT NULL DEFAULT '',
    server       TEXT    NOT NULL DEFAULT '',
    method       TEXT    NOT NULL DEFAULT '', -- tools/call, prompts/get, resources/read
    item         TEXT    NOT NULL DEFAULT '', -- tool, prompt or resource name upstream
    outcome      TEXT    NOT NULL,           -- ok | tool_error | error | refused
    error_class  TEXT    NOT NULL DEFAULT '',
    decide_us    INTEGER NOT NULL DEFAULT 0, -- rules and approval, without the wait
    wait_us      INTEGER NOT NULL DEFAULT 0, -- held for a person's approval
    audit_us     INTEGER NOT NULL DEFAULT 0,
    connect_us   INTEGER NOT NULL DEFAULT 0,
    upstream_us  INTEGER NOT NULL DEFAULT 0,
    total_us     INTEGER NOT NULL DEFAULT 0,
    args_bytes   INTEGER NOT NULL DEFAULT 0,
    result_bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, id)
);
CREATE INDEX call_stats_org_ts ON call_stats (org_id, ts_ms);

CREATE TABLE diagnostics (
    org_id   TEXT    NOT NULL REFERENCES orgs(id),
    id       TEXT    NOT NULL,
    ts_ms    INTEGER NOT NULL,
    level    TEXT    NOT NULL,           -- info | warn | error
    category TEXT    NOT NULL,           -- upstream | grant | gateway | audit | lifecycle
    server   TEXT    NOT NULL DEFAULT '',
    user_id  TEXT    NOT NULL DEFAULT '',
    message  TEXT    NOT NULL,
    repeats  INTEGER NOT NULL DEFAULT 1, -- occurrences this row stands for
    PRIMARY KEY (org_id, id)
);
CREATE INDEX diagnostics_org_ts ON diagnostics (org_id, ts_ms);

-- +goose Down
DROP TABLE diagnostics;
DROP TABLE call_stats;
