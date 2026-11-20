-- +goose Up
-- Token names are unique per person, not in the whole org: two people may
-- each call their token claude-code. Tokens of no member (service tokens,
-- user_id '') stay unique among themselves.
DROP INDEX api_tokens_org_name;
CREATE UNIQUE INDEX api_tokens_org_user_name ON api_tokens (org_id, user_id, name) WHERE revoked_at IS NULL;

-- +goose Down
DROP INDEX api_tokens_org_user_name;
CREATE UNIQUE INDEX api_tokens_org_name ON api_tokens (org_id, name) WHERE revoked_at IS NULL;
