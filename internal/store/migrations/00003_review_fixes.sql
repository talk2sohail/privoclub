-- +goose Up
-- Token kinds (gateway tokens for AI clients, control tokens for people using
-- the admin API) and the person a token belongs to, so self-approval is
-- checked per person, not per token.
ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'gateway';
ALTER TABLE api_tokens ADD COLUMN person TEXT NOT NULL DEFAULT '';
ALTER TABLE approvals ADD COLUMN requester_person TEXT NOT NULL DEFAULT '';
-- Preset rules are tied to the reviewed definition they were built from.
ALTER TABLE policies ADD COLUMN pin_hash TEXT NOT NULL DEFAULT '';
-- Who approved a call, on the call's own audit row.
ALTER TABLE audit_events ADD COLUMN approved_by TEXT NOT NULL DEFAULT '';
-- At most one open approval per exact call.
CREATE UNIQUE INDEX approvals_open ON approvals (org_id, requester_id, server_id, tool, args_digest)
    WHERE status IN ('pending', 'approved', 'denied');

-- +goose Down
DROP INDEX approvals_open;
ALTER TABLE audit_events DROP COLUMN approved_by;
ALTER TABLE policies DROP COLUMN pin_hash;
ALTER TABLE approvals DROP COLUMN requester_person;
ALTER TABLE api_tokens DROP COLUMN person;
ALTER TABLE api_tokens DROP COLUMN kind;
