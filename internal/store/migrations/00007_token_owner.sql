-- +goose Up
-- F27: a token belongs to a member by user id, never by the person name it
-- carries. user_id is set only when a signed-in member makes a token for
-- themselves; '' is a token of nobody (a service token, or one made before
-- this migration). Existing rows stay '' on purpose: which of them a member
-- made for themselves cannot be told from the rows, and guessing from the
-- person name would keep the hole this closes (independent review
-- 2026-11-03, F07 R-01).
ALTER TABLE api_tokens ADD COLUMN user_id TEXT NOT NULL DEFAULT '';
CREATE INDEX api_tokens_org_user ON api_tokens (org_id, user_id) WHERE user_id <> '';

-- Whose account and teams a call ran as: the member of the calling token, ''
-- for a token of nobody. Admin events of a signed-in person already name the
-- user in actor_id.
ALTER TABLE audit_events ADD COLUMN user_id TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE audit_events DROP COLUMN user_id;
DROP INDEX api_tokens_org_user;
ALTER TABLE api_tokens DROP COLUMN user_id;
